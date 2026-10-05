// Browser parity for agent proposals (envelope.StatusProposal, MEL-521).
// Synthetic only; no network or model. stdin: {vectors: [{name, inner,
// valid}]} from proposal_browser_test.go (envelope.TestProposalShapeVectors,
// Go field names); stdout: verdict JSON.
import * as wire from '../static/wire.mjs';

const { vectors } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));
const shapes = {};
for (const { name, inner } of vectors) {
  const n = { reply_to: '', status: '', conv: '', lid: '', sub: '', replica: false, origin: '', emotion: '', target: null, pid: '', fan: null, ref: null, agent_id: '', receiver_route: null, human: null,
    ...inner, root: inner.root ? JSON.stringify(inner.root) : '', attachments: inner.attachments || [] };
  try { await wire.checkVersion2(n); shapes[name] = true; } catch (e) { shapes[name] = false; }
}
console.log(JSON.stringify({ shapes }));
