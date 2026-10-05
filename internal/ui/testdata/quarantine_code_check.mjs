// Held-back codes in the browser (ui.QuarantineItem.Code): the engine names
// every quarantine reason with exactly the code the daemon gives it
// (live.go holdCode), and its sentences send no one to a terminal. Reads
// {reasons: {reason: code}} from the Go test on stdin.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { holdCode } from '../static/engine.mjs';

const { reasons } = JSON.parse(readFileSync(0, 'utf8'));
let checks = 0;
for (const [reason, code] of Object.entries(reasons)) {
  assert.equal(holdCode(reason), code, 'code for ' + JSON.stringify(reason));
  checks++;
}
// Inherited object keys are reasons like any other unknown one.
for (const reason of ['toString', 'constructor', '__proto__']) { assert.equal(holdCode(reason), 'unverified'); checks++; }
const src = readFileSync(new URL('../static/engine.mjs', import.meta.url), 'utf8');
assert.ok(!/agentnet trust/.test(src), 'the engine still sends people to "agentnet trust"');
checks++;
console.log(JSON.stringify({ checks }));
