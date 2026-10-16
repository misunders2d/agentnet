import assert from 'node:assert/strict';
import { nativeActivityDetail, currentNativeActivity } from '../web/src/features/NativeActivity.model.ts';

const detail='Reading files\nReported at 2026-10-15T22:00:00Z';
assert.deepEqual(nativeActivityDetail(detail),{label:'Reading files',at:'2026-10-15T22:00:00Z'});
assert.deepEqual(nativeActivityDetail('Using a tool'),{label:'Using a tool',at:undefined});
for (const label of ['Verifying service recovery status', 'Reviewing the latest test results']) {
 const summary = `Thinking: ${label}\nReported at 2026-10-15T22:00:00Z`;
 assert.deepEqual(nativeActivityDetail(summary), {label, at:'2026-10-15T22:00:00Z'});
 assert.equal(currentNativeActivity(summary, undefined, true), summary);
 assert.equal(currentNativeActivity(undefined, {state:'running', detail:summary}, true), summary);
 assert.equal(currentNativeActivity(undefined, {state:'running', detail:summary, stale:true}, true), '');
 assert.equal(currentNativeActivity(summary, undefined, false), '');
}
for (const value of ['', 'Checking https://example.com/private', 'Reviewing user@example.com', 'Checking /root/.pi/agent/auth.json', 'Reviewing internal/client/worker.go', 'Checking @someone now', 'Checking @channel now', 'Checking bearer token abc', 'Using key AKIA1234567890ABCD12', 'Checking a123456789012345', 'Checking ｓｔａｔｕｓ', 'Reviewing ﬁnal behavior', 'Reviewing 𝒇𝒊𝒏𝒂𝒍 behavior', 'Checking e\u0301tat', 'Reviewing SSN 123-45-6789', 'Analyzing the patient medical diagnosis', 'Using `raw` code', 'Checking '+ 'x'.repeat(121), 'Checking\nprivate', 'Checking\u202e hidden direction text']) {
 assert.equal(nativeActivityDetail(`Thinking: ${value}`), null, 'summary grammar rejects unsafe/noncanonical detail');
}
for(const raw of ['PRIVATE','Reading files PRIVATE','Reading files\nReported at PRIVATE',detail+'\nPRIVATE','Waiting for native input','Waiting for other agents'])
 assert.equal(nativeActivityDetail(raw),null,'only the fixed public grammar is activity');

const remote={state:'running',detail};
assert.equal(currentNativeActivity(undefined,remote,true),detail,'current remote status supplies activity');
assert.equal(currentNativeActivity(detail,undefined,true),detail,'current local device/DM/group annotation supplies activity');
assert.equal(currentNativeActivity('Editing files',remote,true),'Editing files','local current run wins over a remote snapshot');
for(const state of ['answered','done','resolved','needs_human','cancelled','interrupted'])
 assert.equal(currentNativeActivity(undefined,{...remote,state},true),'','non-running status cannot retain old activity');
assert.equal(currentNativeActivity(undefined,{...remote,stale:true},true),'','stale remote status uses generic Working fallback');
assert.equal(currentNativeActivity(undefined,undefined,true),'','unsupported native detail uses generic Working fallback');
assert.equal(currentNativeActivity(detail,remote,false),'','terminal/approval card never displays native activity');
assert.equal(currentNativeActivity('Waiting for native input',remote,true),'','native input takes precedence');
assert.equal(currentNativeActivity('Waiting for other agents',remote,true),'','room wait takes precedence');
assert.equal(currentNativeActivity('Running since 2026-10-15T22:00:00Z',remote,true),'','existing generic local detail stays intact');
console.log('Native activity current local/remote/wait/stale/fallback model PASS');

for (const value of ['Checking a12345678901234', 'Checking status', 'Reviewing final behavior', 'Checking état']) {
 assert.equal(nativeActivityDetail(`Thinking: ${value}`)?.label, value, 'Go/TS canonical normalization and mixed-token bounds agree');
}
