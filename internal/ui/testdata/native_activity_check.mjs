import assert from 'node:assert/strict';
import { nativeActivityDetail, currentNativeActivity } from '../web/src/features/NativeActivity.model.ts';

const detail='Reading files\nReported at 2026-10-15T22:00:00Z';
assert.deepEqual(nativeActivityDetail(detail),{label:'Reading files',at:'2026-10-15T22:00:00Z'});
assert.deepEqual(nativeActivityDetail('Using a tool'),{label:'Using a tool',at:undefined});
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
