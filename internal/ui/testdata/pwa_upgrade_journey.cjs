// Real updater download/replace/Unix exec and installed-window recovery.
// Only executor and local release origin are synthetic; no historical migration claim.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync, spawn } = require('node:child_process');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_PWA_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS;
const binary = process.env.AGENTNET_PWA_BINARY;
assert.equal(binary, path.join(world, 'install/agentnet'), 'fixture must never resolve global installed binary');
const digest = b => createHash('sha256').update(b).digest('hex');
const cli = (who, ...args) => execFileSync(binary, ['--home', path.join(world, who), ...args], { env: process.env, encoding: 'utf8' });
const entry = who => cli(who, 'ui').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const origin = new URL(entry('alice')).origin, bobOrigin = new URL(entry('bob')).origin;
const manifestId = origin + '/', pid = Number(fs.readFileSync(path.join(world, 'alice-pid'), 'utf8'));
const bobPID = Number(fs.readFileSync(path.join(world, 'bob-pid'), 'utf8'));
const expected = Object.fromEntries(fs.readFileSync(process.env.AGENTNET_PWA_ASSET_MANIFEST, 'utf8').trim().split('\n').map(l => { const [h, f] = l.split(/\s+/); return [path.basename(f), h]; }));
const oldHash = digest(fs.readFileSync(path.join(world, 'old-agentnet'))), newHash = digest(fs.readFileSync(path.join(world, 'new-agentnet')));
const proc = n => ({ pid: n, hash: digest(fs.readFileSync('/proc/' + n + '/exe')), inode: fs.statSync('/proc/' + n + '/exe').ino });
const harness = () => fs.existsSync(path.join(world, 'harness.jsonl')) ? fs.readFileSync(path.join(world, 'harness.jsonl'), 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
const starts = () => harness().filter(x => x.event === 'start');
const result = { pass: false, sameSourceVersionStampsOnly: true, oldHash, newHash, origin, errors: [], external: [], assets: {}, screenshots: [], checks: [] };
let context, bobContext, session, app, anchor, bob, updater, stage = 'installed local upgrade setup';
const reads = [], idle = ms => new Promise(r => setTimeout(r, ms));
const ready = p => p.waitForFunction(() => typeof state !== 'undefined' && state.overview && !loading, null, { timeout: 25000 });
const api = (p, url) => p.evaluate(url => window.agentnet.api(url), url);
async function until(label, fn, timeout = 30000) { for (let end = Date.now() + timeout; Date.now() < end;) { if (await fn()) return; await idle(100); } throw Error('timeout: ' + label); }
async function capture(name) {
  for (const width of [390, 1280]) {
    await app.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    const sizes = await app.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth, dialog: [...document.querySelectorAll('dialog[open]')].some(d => d.scrollWidth > d.clientWidth + 1) }));
    assert(!sizes.document && !sizes.dialog, name + ' overflow');
    const file = path.join(evidence, name + '-' + width + '.png'); await app.screenshot({ path: file }); result.screenshots.push(file);
  }
}
const snapshot = () => JSON.parse(execFileSync('python3', ['-c', `import sqlite3,json,sys
c=sqlite3.connect('file:'+sys.argv[1]+'?mode=ro',uri=True)
r={k:c.execute('SELECT * FROM '+k+' ORDER BY 1').fetchall() for k in ['approvals','operators','task_grants']}
r['responder']=c.execute("SELECT v FROM config WHERE k='responder'").fetchone()
r['schema']=c.execute('PRAGMA user_version').fetchone()[0]
print(json.dumps(r))`, path.join(world, 'alice/agent.db')], { encoding: 'utf8' }));
const job = id => JSON.parse(execFileSync('python3', ['-c', `import sqlite3,json,sys
c=sqlite3.connect('file:'+sys.argv[1]+'?mode=ro',uri=True)
print(json.dumps({'state':c.execute('SELECT state FROM inbox WHERE id=?',(sys.argv[2],)).fetchone()[0],'results':c.execute('SELECT count(*) FROM outbox WHERE reply_to=?',(sys.argv[2],)).fetchone()[0]}))`, path.join(world, 'alice/agent.db'), id], { encoding: 'utf8' }));
async function newTask(body) {
  await bob.locator('#nav-chats').click(); await bob.locator('#new-btn').click();
  await bob.locator('#new-to').fill('admin/laptop'); await bob.locator('#new-kind').selectOption('task'); await bob.locator('#new-body').fill(body); await bob.locator('#dialog-ok').click();
  await bob.locator('#dialog').waitFor({ state: 'hidden' }); await bob.waitForFunction(body => state.data?.messages.some(m => m.body === body), body);
  return bob.evaluate(body => state.data.messages.find(m => m.body === body).id, body);
}
async function accept(id) {
  await app.evaluate(id => { location.hash = '#msg=' + id; }, id);
  await app.waitForFunction(id => state.data?.messages.some(m => m.id === id), id);
  await app.locator('#timeline').getByRole('button', { name: 'Accept and run…', exact: true }).click();
  await app.locator('#gate').check(); await app.locator('#dialog-ok').click(); await app.locator('#dialog').waitFor({ state: 'hidden' });
}
(async () => {
  fs.mkdirSync(evidence, { recursive: true, mode: 0o700 }); assert.notEqual(oldHash, newHash);
  assert.equal(proc(pid).hash, oldHash); assert.equal(proc(bobPID).hash, oldHash); result.beforeProcess = proc(pid);
  const env = { ...process.env, HOME: path.join(world, 'browser-home') }; fs.mkdirSync(env.HOME);
  for (const [k, d] of Object.entries({ XDG_CONFIG_HOME: 'config', XDG_CACHE_HOME: 'cache', XDG_DATA_HOME: 'data' })) { env[k] = path.join(env.HOME, d); fs.mkdirSync(env[k]); }
  context = await chromium.launchPersistentContext(path.join(world, 'chromium-profile'), { headless: true, executablePath: process.env.AGENTNET_CHROMIUM, env, viewport: { width: 1280, height: 900 }, args: ['--disable-background-networking', '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1'] });
  await context.route('**/*', r => { const o = new URL(r.request().url()).origin; if ([origin, bobOrigin].includes(o)) return r.continue(); result.external.push(o); return r.abort(); });
  const watch = p => { p.setDefaultTimeout(20000); p.on('pageerror', e => result.errors.push(e.message)); p.on('response', r => { const n = new URL(r.url()).pathname.split('/').pop(); if (expected[n]) reads.push(r.body().then(b => { assert.equal(digest(b), expected[n], n); result.assets[n] = digest(b); }).catch(e => result.errors.push(e.message))); }); };
  context.on('page', watch); for (const p of context.pages()) watch(p);
  anchor = context.pages()[0]; await anchor.goto(entry('alice')); await ready(anchor);
  assert.equal(await anchor.locator('link[rel=manifest]').count(), 1); assert.equal(await anchor.locator('link[rel=manifest]').getAttribute('crossorigin'), 'use-credentials');
  result.manifest = await anchor.evaluate(async () => { const r = await fetch(document.querySelector('link[rel=manifest]').href); if (!r.ok) throw Error('credentialed native manifest refused'); return { status: r.status, type: r.headers.get('content-type'), data: await r.json() }; });
  assert(result.manifest.type.includes('manifest+json')); for (const k of ['id', 'start_url', 'scope']) assert.equal(new URL(result.manifest.data[k], origin).href, manifestId);
  session = await context.browser().newBrowserCDPSession(); const pageCDP = await context.newCDPSession(anchor);
  await pageCDP.send('PWA.install', { manifestId, installUrlOrBundleUrl: manifestId }); result.installed = await session.send('PWA.getOsAppState', { manifestId });
  await session.send('PWA.changeAppUserSettings', { manifestId, displayMode: 'standalone' });
  [app] = await Promise.all([context.waitForEvent('page'), session.send('PWA.launch', { manifestId })]); await ready(app); assert(await app.evaluate(() => matchMedia('(display-mode: standalone)').matches));
  // Native sessions share a host cookie name; the second product home needs its own isolated context.
  bobContext = await context.browser().newContext();
  await bobContext.route('**/*', r => { const o = new URL(r.request().url()).origin; if (o === bobOrigin) return r.continue(); result.external.push(o); return r.abort(); });
  bob = await bobContext.newPage(); watch(bob); await bob.goto(entry('bob')); await ready(bob);
  // Synthetic room creation only. Message, attachment and task decisions use visible controls.
  const dm = { id: cli('alice', 'dm', 'new', 'bob/desk').trim() };
  await app.evaluate(() => loadOverview()); // CLI setup's new room must enter this page's directory before notification lookup.
  await app.evaluate(id => { location.hash = '#conv=' + id; }, dm.id); await app.waitForFunction(id => state.dm === id && state.dmData, dm.id);
  await app.locator('#body').fill('Upgrade retained conversation'); await app.locator('#send').click(); await app.waitForFunction(() => !state.sending && !document.getElementById('body').value);
  const bytes = Buffer.from('Upgrade retained exact file bytes\n'); await app.locator('#file-input').setInputFiles({ name: 'upgrade-history.txt', mimeType: 'text/plain', buffer: bytes }); await app.locator('#body').fill('Upgrade retained attachment'); await app.locator('#send').click();
  await app.waitForFunction(() => state.dmData.messages.some(m => m.body === 'Upgrade retained attachment' && m.attachments?.some(f => f.openable)));
  const fileMessage = (await api(app, '/api/dm?id=' + dm.id)).messages.find(m => m.body === 'Upgrade retained attachment');
  const readBytes = () => app.evaluate(async id => Array.from((await window.agentnet.file(id, 0, 'out')).bytes), fileMessage.id);
  assert.deepEqual(Buffer.from(await readBytes()), bytes); result.historyFile = { id: fileMessage.id, bytes: bytes.length, sha256: digest(bytes) };
  result.identity = (await api(app, '/api/overview')).me; await capture('upgrade-installed-history-file');
  stage = 'real running task before updater'; const first = await newTask('Upgrade running task'); await until('task awaiting', () => job(first).state === 'awaiting'); await accept(first); await until('stub running', () => fs.existsSync(path.join(world, 'running-started'))); assert.equal(job(first).state, 'running');
  const pending = await newTask('Upgrade pending task'); await until('second awaiting', () => job(pending).state === 'awaiting');
  cli('alice', 'approve', 'bob/desk'); cli('alice', 'approve', '--tasks', 'bob/desk'); cli('alice', 'operator', 'grant', 'bob/desk'); result.beforePersistence = snapshot(); assert(result.beforePersistence.task_grants.length && result.beforePersistence.operators.length && result.beforePersistence.approvals.length);
  await app.evaluate(id => { location.hash = '#conv=' + id; }, dm.id); await app.waitForFunction(id => state.dm === id, dm.id);
  await app.locator('#body').fill('Unsent exact upgrade draft'); const unsentBytes = Buffer.from('Unsent upgrade file bytes\n'); await app.locator('#file-input').setInputFiles({ name: 'upgrade-unsent.txt', mimeType: 'text/plain', buffer: unsentBytes });
  await app.evaluate(async () => { setDMReply(state.dmData.messages.find(m => m.body === 'Upgrade retained conversation')); await preparedFiles(state.files, window.agentnet); window.oldUpgradeHost = window.agentnet; });
  result.beforeDraft = await app.evaluate(() => ({ text: document.getElementById('body').value, reply: state.dmReply.id, kind: kindValue(), target: targetId(), files: state.files.map(({ name, size, staged }) => ({ name, size, staged })), handle: window.agentnet.workspace.handle }));
  stage = 'actual updater download and pending switch'; let stdout = '', stderr = '';
  updater = spawn(binary, ['--home', path.join(world, 'alice'), 'update', 'v9.9.9'], { env: process.env, stdio: ['ignore', 'pipe', 'pipe'] }); updater.stdout.on('data', b => { stdout += b; }); updater.stderr.on('data', b => { stderr += b; });
  const updateDone = new Promise((resolve, reject) => { updater.on('error', reject); updater.on('exit', code => resolve(code)); });
  await until('real executable replaced and update request', () => digest(fs.readFileSync(binary)) === newHash && fs.existsSync(path.join(world, 'alice/update-request.json')));
  assert.equal(proc(pid).hash, oldHash); assert.equal(starts().length, 1); assert.equal(job(first).state, 'running'); assert.equal(job(pending).state, 'awaiting'); result.pendingProcess = proc(pid);
  result.pendingRequest = JSON.parse(fs.readFileSync(path.join(world, 'alice/update-request.json'), 'utf8')); assert.equal(result.pendingRequest.exe, binary); assert.equal(result.pendingRequest.to, 'v9.9.9'); await capture('upgrade-running-job-pending');
  fs.writeFileSync(path.join(world, 'release-running'), 'explicit synthetic fixture release\n');
  await until('real Unix exec into new bytes and activation', () => { const file = path.join(world, 'alice/update-activation.json'); return fs.existsSync(file) && JSON.parse(fs.readFileSync(file)).running === 'v9.9.9' && proc(pid).hash === newHash; });
  const updateCode = await updateDone; fs.writeFileSync(path.join(evidence, 'updater-output.txt'), stdout + stderr, { mode: 0o600 }); assert.equal(updateCode, 0); assert(stdout.includes('now runs agentnet v9.9.9'));
  result.afterProcess = proc(pid); assert.notEqual(result.beforeProcess.inode, result.afterProcess.inode); assert.equal(proc(bobPID).hash, oldHash); result.activation = JSON.parse(fs.readFileSync(path.join(world, 'alice/update-activation.json'))); assert.equal(result.activation.result, 'running'); assert.equal(result.activation.pid, pid); assert(!fs.existsSync(path.join(world, 'alice/update-request.json')));
  assert.equal(job(first).state, 'answered'); assert.equal(job(first).results, 1); assert.equal(job(pending).state, 'awaiting'); assert.equal(job(pending).results, 0); assert.equal(starts().length, 1); assert.equal(harness().filter(x => x.event === 'done').length, 1);
  result.afterPersistence = snapshot(); assert.deepEqual(result.afterPersistence, result.beforePersistence); result.jobsBeforeReconnect = { first: job(first), pending: job(pending), runs: starts().length }; result.checks.push('actual TLS download/checksum/replace/pending/exec + identical persisted grants/responder/schema + no task rerun');
  stage = 'supported installed authenticated update handoff';
  // Unlike an ordinary restart, a genuine matched update preserves its UI session and port.
  result.handoffStatus = await app.evaluate(async () => (await fetch('/api/overview')).status);
  assert.equal(result.handoffStatus, 200, 'existing authenticated update handoff');
  await until('installed recovered workspace', async () => (await app.evaluate(() => typeof state !== 'undefined' && state.overview?.version === 'v9.9.9' && document.getElementById('lost').hidden)));
  result.afterDraft = await app.evaluate(() => ({ text: document.getElementById('body').value, reply: state.dmReply?.id, kind: kindValue(), target: targetId(), files: state.files.map(({ name, size, staged, reattachRequired }) => ({ name, size, staged, reattachRequired })), handle: window.agentnet.workspace.handle, fingerprint: state.overview.me.fingerprint, version: state.overview.version }));
  await capture('upgrade-supported-handoff-draft');
  for (const k of ['text', 'reply', 'kind', 'target']) assert.deepEqual(result.afterDraft[k], result.beforeDraft[k], 'retained exact draft ' + k);
  assert.equal(result.afterDraft.fingerprint, result.identity.fingerprint); assert.notEqual(result.afterDraft.handle, result.beforeDraft.handle);
  assert.deepEqual(result.afterDraft.files, [{ name: 'upgrade-unsent.txt', size: unsentBytes.length, staged: '', reattachRequired: true }], 'upgrade unsent file must survive exact handoff');
  assert.deepEqual(Buffer.from(await readBytes()), bytes); const history = await api(app, '/api/dm?id=' + dm.id); assert(history.messages.some(m => m.body === 'Upgrade retained conversation'));
  await app.locator('#send').click(); assert((await app.locator('#compose-error').innerText()).includes('reattach')); assert.equal(await app.locator('#body').inputValue(), result.beforeDraft.text);
  await app.getByRole('button', { name: 'Remove upgrade-unsent.txt', exact: true }).click(); await app.locator('#file-input').setInputFiles({ name: 'upgrade-unsent.txt', mimeType: 'text/plain', buffer: unsentBytes }); await app.locator('#send').click(); await app.waitForFunction(() => !state.sending && !document.getElementById('body').value);
  const sent = (await api(app, '/api/dm?id=' + dm.id)).messages.filter(m => m.body === result.beforeDraft.text); assert.equal(sent.length, 1); assert.equal(sent[0].reply_to, result.beforeDraft.reply); assert.deepEqual(Buffer.from(await app.evaluate(async id => Array.from((await window.agentnet.file(id, 0, 'out')).bytes), sent[0].id)), unsentBytes); await capture('upgrade-explicit-reattach-send-once');
  await accept(pending); await until('post-upgrade pending task explicit run', () => job(pending).state === 'answered'); assert.equal(job(pending).results, 1); assert.equal(starts().length, 2); result.checks.push('installed draft/files/history safe; explicit pending acceptance runs once with retained responder');
  result.finalJobs = { first: job(first), pending: job(pending), runs: starts().length }; result.releaseRequests = fs.readFileSync(path.join(world, 'release-requests.jsonl'), 'utf8').trim().split('\n').map(JSON.parse); assert(result.releaseRequests.every(x => x.tls && x.path.startsWith('/releases/download/v9.9.9/')));
  await Promise.all(reads); assert.deepEqual(result.errors, []); assert.deepEqual(result.external, []); await app.close(); await session.send('PWA.uninstall', { manifestId }); result.uninstalled = true; result.pass = true;
  console.log('PASS actual private updater/exec/PWA handoff, same-source stamps only');
})().catch(async e => {
  result.stage = stage; result.error = e.stack; console.error('FAIL ' + stage + ': ' + e.message);
  if (app) { await app.screenshot({ path: path.join(evidence, 'upgrade-first-failure.png') }).catch(() => {}); fs.writeFileSync(path.join(evidence, 'upgrade-first-failure.txt'), await app.locator('body').innerText().catch(() => ''), { mode: 0o600 }); }
  process.exitCode = 1;
}).finally(async () => {
  if (bobContext) await bobContext.close(); if (context) await context.close(); if (updater && updater.exitCode === null) updater.kill();
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify(result, null, 2), { mode: 0o600 }); console.log(JSON.stringify({ pass: result.pass, stage: result.stage, error: result.error, checks: result.checks }));
});
