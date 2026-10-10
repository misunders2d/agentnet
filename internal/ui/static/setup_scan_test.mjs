import assert from 'node:assert/strict';
import {canScanInvitation, scanInvitation} from './setup.mjs';

assert.equal(canScanInvitation('none'),true);
assert.equal(canScanInvitation('incomplete'),true);
for (const state of ['pending','enrolled','refused','expired','removed','unknown'])
  assert.equal(canScanInvitation(state),false,`${state} must preserve its existing enrollment`);
let calls=0;
const text='https://workspace.example/#'+encodeURIComponent('agentnet-link-v2:untrusted');
assert.equal(await scanInvitation(async()=>{calls++;return {text};}),'agentnet-link-v2:untrusted');
assert.equal(calls,1,'one explicit gesture invokes one scan');
assert.equal(await scanInvitation(async()=>({text:'agentnet-invite-v1:untrusted'})),'agentnet-invite-v1:untrusted');
assert.equal(await scanInvitation(async()=>({canceled:true,text:'unrelated'})),'','cancel returns no replacement text');
for (const result of [{text:'https://unrelated.example/'},{text:''},{text:42},{text:'x'.repeat(16*1024+1)},
  {text:'agentnet-link-v2:'+'€'.repeat(6000)},{}])
  await assert.rejects(()=>scanInvitation(async()=>result),/not an AgentNet invitation/);
await assert.rejects(()=>scanInvitation(async()=>{throw Error('Camera permission declined');}),/Camera permission declined/);
console.log('Android setup QR: normalization, cancellation, payload bounds and pending enrollment guards passed');
