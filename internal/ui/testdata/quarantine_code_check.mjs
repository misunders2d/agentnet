// Held-back codes in the browser (ui.QuarantineItem.Code): the engine names
// every quarantine reason with exactly the code the daemon gives it
// (live.go holdCode), and its sentences send no one to a terminal. Reads
// {reasons: {reason: code}} from the Go test on stdin.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { holdCode, quarantineItem } from '../static/engine.mjs';

const { reasons } = JSON.parse(readFileSync(0, 'utf8'));
let checks = 0;
for (const [reason, code] of Object.entries(reasons)) {
  assert.equal(holdCode(reason), code, 'code for ' + JSON.stringify(reason));
  checks++;
}
// Inherited object keys are reasons like any other unknown one.
for (const reason of ['toString', 'constructor', '__proto__']) {
  assert.equal(holdCode(reason), 'unverified');
  assert.equal(quarantineItem({ id: 'h', from: 'alice/laptop', reason, at: 0 }).reason, 'It did not verify, so its content is not shown.');
  checks++;
}
// The overview's held-back item carries the code, as the daemon's does (live.go quarantineItems).
const at = Date.UTC(2026, 9, 5, 12, 0, 0);
for (const [reason, code] of Object.entries(reasons)) {
  const item = quarantineItem({ id: 'held-' + reason, from: 'alice/laptop', reason, at });
  assert.deepEqual(Object.keys(item).sort(), ['at', 'code', 'id', 'peer', 'reason'], 'item fields for ' + JSON.stringify(reason));
  assert.equal(item.code, code, 'item code for ' + JSON.stringify(reason));
  assert.equal(item.peer, 'alice/laptop');
  assert.equal(item.at, new Date(at).toISOString());
  checks++;
}
const src = readFileSync(new URL('../static/engine.mjs', import.meta.url), 'utf8');
assert.ok(!/agentnet trust/.test(src), 'the engine still sends people to "agentnet trust"');
checks++;
console.log(JSON.stringify({ checks }));
