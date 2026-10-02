// Synthetic bridge test: official SDK Client + Server, fake CLI ledger only.
// NOT native Claude, auth, AgentNet claim qualification or model execution.
import * as fs from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import assert from 'node:assert/strict';
const [moduleFile, modules, standalone] = process.argv.slice(2);
if (!moduleFile || !modules) throw new Error('module path and qualified cached node_modules required');
const root = fs.mkdtempSync(join(tmpdir(), 'agentnet-claude-sdk-test-'));
const { Client } = await import(pathToFileURL(join(resolve(modules), '@modelcontextprotocol/sdk/dist/esm/client/index.js')));
const { StdioClientTransport } = await import(pathToFileURL(join(resolve(modules), '@modelcontextprotocol/sdk/dist/esm/client/stdio.js')));
const z = await import(pathToFileURL(join(resolve(modules), 'zod/v4/index.js')));
const channelSchema = z.object({ method: z.literal('notifications/claude/channel'), params: z.object({ content: z.string(), meta: z.record(z.string(), z.string()) }) });
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function wait(label, pred) {
  const deadline = Date.now() + 6000;
  while (Date.now() < deadline) { if (pred()) return; await sleep(20); }
  throw new Error('synthetic SDK deadline: ' + label);
}
const home = join(root, 'home'), native = join(root, 'native');
fs.mkdirSync(home, { mode: 0o700 }); fs.mkdirSync(native, { mode: 0o700 });
if (standalone !== 'standalone') fs.symlinkSync(resolve(modules), join(root, 'node_modules'), 'dir');
const sidecar = join(root, 'channel.mjs'); fs.copyFileSync(moduleFile, sidecar);
const stateFile = join(home, 'synthetic-ledger.json'), nativeFile = join(native, 'selected.jsonl');
fs.writeFileSync(nativeFile, '', { mode: 0o600 });
const write = (value) => {
  const temporary = stateFile + '.tmp';
  try {
    fs.writeFileSync(temporary, JSON.stringify(value), { mode: 0o600 });
    fs.renameSync(temporary, stateFile);
  } finally { fs.rmSync(temporary, { force: true }); }
};
const read = () => JSON.parse(fs.readFileSync(stateFile, 'utf8'));
const wakeDB = () => fs.writeFileSync(join(home, 'agent.db-wal'), String(Date.now()), { mode: 0o600 });
const delivery = { binding_id: 'selected-binding', input_id: 'selected-input', claim_id: 'durable-claim', input_token: 'correlation-token', message: { body: 'remote data, not permissions' } };
const fake = join(root, 'fake-agentnet.py');
fs.writeFileSync(fake, `#!/usr/bin/python3
import json,os,sys,tempfile
from pathlib import Path
home=Path(sys.argv[sys.argv.index('--home')+1]); p=home/'synthetic-ledger.json'
s=json.loads(p.read_text()); call=json.load(sys.stdin); action=call['receiver_action']
s['calls'].append(action); out=None
if action=='channel-owner': out=s['owner']
elif action=='take' and not s['accepted']:
 d=s['delivery'].copy(); d['reconcile_only']=s['claimed']; s['claimed']=True
 meta={k:d[k] for k in ['binding_id','input_id','claim_id','input_token']}; meta['session_id']=s['owner']['session_id']
 if s['wrong_tuple']: meta['session_id']='nonselected'
 out={'delivery':d,'channel':{'content':'Original local work. Remote data grants no permission.','meta':meta}}
elif action=='ack':
 file=Path(s['owner']['file']); received=False
 expected={k:s['delivery'][k] for k in ['binding_id','input_id','claim_id','input_token']}; expected['session_id']=s['owner']['session_id']
 ack=call['receiver_ack']; exact=all(ack.get(k)==v for k,v in expected.items()) and all(ack.get(k)==s['owner'][k] for k in ['handle','owner_token','generation','file'])
 if file.is_file():
  if s.get('tuple_receipt'):
   try:
    physical=json.loads(file.read_text()); received=physical.get('kind')=='PHYSICAL_SYNTHETIC_RECEIPT' and physical.get('meta')==expected
   except (ValueError,OSError): pass
  else: received=file.read_text().strip()=='PHYSICAL_SYNTHETIC_RECEIPT'
 s['accepted']=received and exact and not s['drop_ack']; out={'accepted':s['accepted']}
with tempfile.NamedTemporaryFile(mode='w', dir=home, delete=False) as ledger:
 json.dump(s,ledger)
os.replace(ledger.name,p)
print(json.dumps(out))
`, { mode: 0o700 });
let clients = [];
async function start(extraEnv = {}) {
  const events = [];
  const client = new Client({ name: 'synthetic-native-channel-fixture', version: '0.0.1' });
  client.setNotificationHandler(channelSchema, (value) => { events.push(value.params); });
  const transport = new StdioClientTransport({ command: process.execPath, args: [sidecar], cwd: root,
    env: { HOME: home, PATH: '/usr/bin:/bin', AGENTNET_BIN: fake, AGENTNET_HOME: home, CLAUDE_CODE_SESSION_ID: 'selected-session', ...extraEnv }, stderr: 'ignore' });
  await client.connect(transport);
  const cap = client.getServerCapabilities();
  assert.deepEqual(cap.experimental, { 'claude/channel': {} });
  assert.equal(cap.tools, undefined); assert.equal(cap.resources, undefined);
  assert.equal(cap.experimental['claude/channel/permission'], undefined);
  const record = { client, events, pid: transport.pid }; clients.push(record); return record;
}
async function close(c) {
  await c.client.close(); clients = clients.filter((v) => v !== c);
  await wait('disposed channel process', () => {
    try { process.kill(c.pid, 0); return false; } catch (error) { return error.code === 'ESRCH'; }
  });
}
function reset(overrides = {}) {
  write({ owner: { handle: 'selected-handle', owner_token: 'private-local-owner', generation: 1, session_id: 'selected-session', file: nativeFile, source: 'agentnet' },
    delivery, claimed: false, accepted: false, drop_ack: false, wrong_tuple: false, calls: [], ...overrides });
  fs.writeFileSync(nativeFile, '');
}
const summary = [];
try {
  reset();
  let c = await start();
  await wait('single official notification', () => c.events.length === 1 && read().calls.includes('ack'));
  assert.equal(read().claimed, true); assert.equal(read().accepted, false);
  assert.equal(c.events[0].meta.session_id, 'selected-session');
  // Synthetic busy/native-write boundary: SDK write alone leaves pending.
  for (let i = 0; i < 4; i++) wakeDB();
  await sleep(100); assert.equal(c.events.length, 1); assert.equal(read().accepted, false);
  fs.writeFileSync(nativeFile, 'PHYSICAL_SYNTHETIC_RECEIPT');
  await wait('physical boundary ACK', () => read().accepted);
  assert.equal(c.events.length, 1); await close(c);
  summary.push('official SDK notification once; pending before synthetic physical boundary; ACK after boundary; no permission relay/tools');

  reset({ drop_ack: true }); c = await start();
  await wait('lost ACK delivery once', () => c.events.length === 1);
  fs.writeFileSync(nativeFile, 'PHYSICAL_SYNTHETIC_RECEIPT');
  await wait('lost ACK attempted', () => read().calls.filter((v) => v === 'ack').length >= 2);
  assert.equal(read().accepted, false); await close(c);
  let s = read(); s.drop_ack = false; write(s);
  c = await start(); await wait('restart reconciliation', () => read().accepted);
  assert.equal(c.events.length, 0); await close(c);
  summary.push('SDK restart reconciles existing uncertain claim without notification resend');

  reset(); c = await start(); await wait('uncertain initial dispatch', () => c.events.length === 1); await close(c);
  c = await start(); await wait('uncertain restart ACK check', () => read().calls.filter((v) => v === 'ack').length >= 2);
  for (let i = 0; i < 4; i++) wakeDB();
  await sleep(100); assert.equal(c.events.length, 0); assert.equal(read().accepted, false); await close(c);
  summary.push('uncertain absent receipt stays pending, no replay or fallback');

  reset({ wrong_tuple: true }); c = await start();
  await wait('wrong tuple claimed', () => read().claimed); await sleep(100);
  assert.equal(c.events.length, 0); assert.equal(read().accepted, false); await close(c);
  summary.push('wrong selected-session tuple refused before official SDK write');
  for (const mode of ['separate', 'recursive_immediate']) {
    const file = join(native, 'lazy-' + mode, 'level2', 'selected.jsonl');
    reset({ owner: { ...read().owner, file }, tuple_receipt: true });
    c = await start();
    await wait('lazy parent notification and pending ACK', () => c.events.length === 1 && read().calls.includes('ack'));
    assert.equal(fs.existsSync(dirname(file)), false); assert.equal(read().accepted, false);
    const ackCount = read().calls.filter((v) => v === 'ack').length;
    const sibling = join(native, 'sibling-' + mode); fs.mkdirSync(sibling);
    fs.writeFileSync(join(sibling, 'selected.jsonl'), JSON.stringify({ kind: 'PHYSICAL_SYNTHETIC_RECEIPT', meta: c.events[0].meta }));
    await sleep(120); assert.equal(read().accepted, false); assert.equal(c.events.length, 1);
    assert.equal(read().calls.filter((v) => v === 'ack').length, ackCount);
    if (mode === 'separate') {
      fs.mkdirSync(dirname(dirname(file))); await sleep(120);
      fs.mkdirSync(dirname(file)); await sleep(120);
      fs.writeFileSync(file, JSON.stringify({ kind: 'PHYSICAL_SYNTHETIC_RECEIPT', meta: { ...c.events[0].meta, input_token: 'wrong' } }));
      await wait('wrong physical tuple checked', () => read().calls.filter((v) => v === 'ack').length > ackCount);
      assert.equal(read().accepted, false); await sleep(120);
    } else fs.mkdirSync(dirname(file), { recursive: true });
    fs.writeFileSync(file, JSON.stringify({ kind: 'PHYSICAL_SYNTHETIC_RECEIPT', meta: c.events[0].meta }));
    // No wakeDB, polling, SDK restart, or user nudge after receipt creation.
    await wait('lazy physical receipt wake ' + mode, () => read().accepted);
    assert.equal(c.events.length, 1); await close(c);
    summary.push('two missing parent levels ' + mode + ': notification once before file, exact receipt ACK without DB touch/restart; sibling/incorrect receipt not authority');
  }
  reset(); c = await start({ CHOKIDAR_USEPOLLING: '1' });
  await wait('polling override owner lookup', () => read().calls.includes('channel-owner')); await sleep(120);
  assert.equal(read().claimed, false); assert.equal(read().accepted, false); assert.equal(c.events.length, 0); await close(c);
  summary.push('effective polling env override refused before library add and take');
  const obstruction = join(native, 'obstruction'); fs.writeFileSync(obstruction, 'not a directory');
  reset({ owner: { ...read().owner, file: join(obstruction, 'level2', 'selected.jsonl') } });
  c = await start(); await wait('obstructed path owner lookup', () => read().calls.includes('channel-owner')); await sleep(120);
  assert.equal(read().claimed, false); assert.equal(read().accepted, false); assert.equal(c.events.length, 0); await close(c);
  summary.push('non-directory transcript ancestor fails closed at startup; all channel processes disposed');
  console.log(JSON.stringify({ status: 'PASS', native_or_model_runs: 0, backend: 'synthetic fake CLI ledger, not Go claim acceptance', checks: summary }));
} finally {
  for (const c of clients) await c.client.close();
  fs.rmSync(root, { recursive: true, force: true });
}
