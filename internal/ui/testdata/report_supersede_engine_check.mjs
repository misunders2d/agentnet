// Browser parity for review notice superseding (client.supersedeNotices,
// client.settleLeftoverNotices) and report counts/deciders, decision
// results across a host's reports. Synthetic Engine/store only; no network.
// stdin: {cases} from client.TestReviewSupersedeVectors; stdout: verdicts.
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';

const { cases } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));
const fetch = async () => { throw Error('no network'); };
const id = (i) => (i + 1).toString(16).padStart(32, '0');
const row = (x, i) => ({ id: id(i), v: 1, from: x.from, to: 'me/browser', kind: 'message', status: 'review_notice', body: x.body, ts: x.ts, at: 1000 * (i + 1), read: false, state: '', attachments: undefined });
const verdicts = {};
for (const c of cases) {
  // Stored one by one as they arrive.
  const e = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', fetch });
  let inbox = [];
  c.notices.forEach((x, i) => {
    for (const op of e.noticeOps(inbox, row(x, i))) inbox = [...inbox.filter((r) => r.id !== op.k), op.v];
    if (x.dismiss) inbox = inbox.map((r) => r.id === id(i) ? { ...r, resolved: true } : r); // the person dismisses it
  });
  const open = c.notices.map((_, i) => i).filter((i) => { const r = inbox.find((x) => x.id === id(i)); return r && !r.resolved; });
  // Stored before superseding: the one-time cleanup on load.
  const store = memoryStore();
  await store.write(c.notices.map((x, i) => ({ s: 'inbox', k: id(i), v: x.dismiss ? { ...row(x, i), resolved: true } : row(x, i) })));
  await store.write([{ s: 'kv', k: 'identity', v: { address: 'me/browser', keys: {}, fingerprint: 'f' } }]);
  const loaded = new Engine({ store, base: 'https://synthetic.invalid', fetch });
  await loaded.load();
  const after = await store.all('inbox');
  const cleanup = c.notices.map((_, i) => i).filter((i) => !after.find((x) => x.id === id(i)).resolved);
  verdicts[c.name] = { open, cleanup_open: cleanup };
  await loaded.load(); // once: nothing more changes
  assert.deepEqual(c.notices.map((_, i) => i).filter((i) => !after.find((x) => x.id === id(i)).resolved), cleanup);
}

// A count report names who decides; a DM item is marked conv.
const e = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', fetch });
const count = row({ from: 'bot/a', ts: 5, body: JSON.stringify({ v: 2, at: 5, host: 'bot/a', items: [], count: 3, deciders: [{ person: 'p'.repeat(64), label: 'Sergey' }, { address: 'ops/desk' }] }) }, 0);
const items = e.reportItems([count]);
assert.equal(items[0].report.count, 3);
assert.deepEqual(items[0].report.deciders, [{ person: 'p'.repeat(64), label: 'Sergey' }, { address: 'ops/desk' }]);
assert.equal(items[0].excerpt, '3 requests reported by bot/a');
assert.equal(e.noticeLine(count), '3 requests reported by bot/a');
const key = 'aaaaaaaa-bbbbbbbb-cccccccc-dddddddd', rid = '7'.repeat(32);
const named = row({ from: 'bot/a', ts: 9, body: JSON.stringify({ v: 2, at: 9, host: 'bot/a', items: [{ id: rid, from: 'me/browser', key, kind: 'task', state: 'awaiting', blocker: 'awaiting_acceptance', since: 1, attempt: 0, actionable: true, conv: true }] }) }, 1);
// A decision made from an older report: its answer shows on the newer card.
const decision = '8'.repeat(32);
const outbox = [{ id: decision, control: true, sub: 'decision', to: 'bot/a', ref: { id: rid, fingerprint: key }, body: JSON.stringify({ action: 'accept', expect: 'awaiting', attempt: 0, text: '', report: id(0) }) }];
const status = { id: '9'.repeat(32), control: true, sub: 'status', from: 'bot/a', ref: { id: rid, fingerprint: key }, body: JSON.stringify({ state: 'queued', n: 1, at: 10, decision, report: id(0), attempt: 0 }) };
const got = e.reportItems([named, status], outbox)[0].report.items[0];
assert.equal(got.conv, true);
assert.equal(got.result?.decision, decision);
// A task carrying out a proposal shows where it comes from (client.ReportItem.proposal).
const proposal = { question_id: 'q'.repeat(32), question: 'is the changelog up to date?', asker: 'me/browser', proposal_id: 'p'.repeat(32), proposal: 'Update CHANGELOG.md for 1.4\nwith the fixes', confirmed_by: 'me/browser' };
const carried = row({ from: 'bot/a', ts: 12, body: JSON.stringify({ v: 2, at: 12, host: 'bot/a', items: [{ id: '6'.repeat(32), from: 'me/browser', key, kind: 'task', state: 'awaiting', blocker: 'awaiting_acceptance', since: 1, attempt: 0, actionable: true, proposal }] }) }, 2);
assert.deepEqual(e.reportItems([carried])[0].report.items[0].proposal, { ...proposal, proposal: 'Update CHANGELOG.md for 1.4' });
// Runtime snapshots preserve the exact request/attempt and existing Stop
// authority while exposing silence as a notice, never a terminal state.
for (const blocker of ['running', 'seems_stuck']) {
  const running = structuredClone(carried), r = JSON.parse(running.body);
  Object.assign(r.items[0], { state: 'running', blocker, attempt: 1, since: 1759500000 });
  running.body = JSON.stringify(r);
  const shown = e.reportItems([running])[0].report.items[0];
  assert.equal(shown.state, 'running'); assert.equal(shown.blocker, blocker);
  assert.equal(shown.actionable, true); assert.equal(shown.attempt, 1);
  assert.deepEqual(shown.proposal, { ...proposal, proposal: 'Update CHANGELOG.md for 1.4' });
}
console.log(JSON.stringify({ verdicts }));
