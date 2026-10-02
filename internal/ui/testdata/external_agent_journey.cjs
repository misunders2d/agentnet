// Real encrypted loopback providers; visible controls perform all user actions.
// CLI only seeds synthetic people/history and approves the synthetic browser link.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_HUMAN_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS;
const authorshipOnly = process.env.AGENTNET_EXTERNAL_AUTHORSHIP_ONLY === '1';
assert(world && evidence && process.env.AGENTNET_HUMAN_BINARY && process.env.AGENTNET_HUMAN_ASSET_MANIFEST);
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const nativeURL = n => fs.readFileSync(path.join(world, n + '-ui'), 'utf8').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const allowed = new Set([origin, ...['alice', 'bob', 'outside'].map(n => new URL(nativeURL(n)).origin)]);
const cli = (n, ...args) => execFileSync(process.env.AGENTNET_HUMAN_BINARY, ['--home', path.join(world, n), ...args], { encoding: 'utf8', env: process.env });
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const expectedAssets = Object.fromEntries(fs.readFileSync(process.env.AGENTNET_HUMAN_ASSET_MANIFEST, 'utf8').trim().split('\n').map(line => { const [hash, name] = line.trim().split(/\s+/); return [path.basename(name), hash]; }));
const errors = [], external = [], shots = [], checks = [], assetReads = [], served = {};
let browser, stage = 'setup';
const idle = ms => new Promise(r => setTimeout(r, ms));
const ready = p => p.waitForFunction(() => typeof state !== 'undefined' && state.overview && state.overview.directory.current, null, { timeout: 25000 });
async function until(label, predicate) {
  for (let i = 0; i < 250; i++) { if (await predicate()) return; await idle(100); }
  throw Error('Timed out: ' + label);
}
async function capture(p, name, width) {
  assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, name + ' overflow');
  assert(await p.evaluate(() => [...document.querySelectorAll('dialog[open]')].every(d => d.scrollWidth <= d.clientWidth + 1)), name + ' dialog overflow');
  const file = path.join(evidence, `external-real-${name}-${width}.png`); await p.screenshot({ path: file }); shots.push(file);
}
async function home(p) {
  if (await p.locator('#settings').isVisible()) await p.locator('#settings-close').click();
  for (let i = 0; i < 3 && !(await p.locator('#nav-chats').isVisible()); i++) await p.locator('#back').click();
  await p.locator('#nav-chats').click(); await p.locator('#search').waitFor({ state: 'visible' });
}
async function openDM(p, id) {
  await home(p);
  await p.waitForFunction(id => state.overview.dms.some(d => d.id === id), id);
  const label = await p.evaluate(id => state.overview.dms.find(d => d.id === id).peer.label, id);
  await p.locator('#conv-list .contact-item').filter({ hasText: label }).locator('button.contact').click();
  if (await p.evaluate(id => state.dm !== id, id)) {
    const summary = await p.evaluate(id => { const d = state.overview.dms.find(d => d.id === id); return { title: d.title, count: d.count }; }, id);
    const rows = p.locator('#hub .thread-row').filter({ hasText: summary.title });
    const row = await rows.count() === 1 ? rows : rows.filter({ has: p.locator('.thread-count').filter({ hasText: new RegExp('^' + summary.count + '$') }) });
    assert.equal(await row.count(), 1, 'visible title/count identifies intended topic');
    await row.click();
  }
  await p.waitForFunction(id => state.dm === id && state.dmData?.id === id, id);
}
const readDM = (p, id) => p.evaluate(id => agentnet.api('/api/dm?id=' + id), id);
const harnessRuns = () => fs.existsSync(path.join(world, 'harness.jsonl')) ? fs.readFileSync(path.join(world, 'harness.jsonl'), 'utf8').trim().split('\n').map(JSON.parse) : [];
(async () => {
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM, args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const pages = {};
  for (const name of ['alice', 'phone', 'outside']) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
    await context.route('**/*', r => { if (allowed.has(new URL(r.request().url()).origin)) return r.continue(); external.push(new URL(r.request().url()).origin); return r.abort(); });
    const p = pages[name] = await context.newPage(); p.setDefaultTimeout(20000);
    p.on('pageerror', e => errors.push(e.stack || e.message));
    p.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
    p.on('response', r => { const asset = new URL(r.url()).pathname.split('/').pop(); if (expectedAssets[asset]) assetReads.push((async () => {
      const hash = digest(await r.body()); assert.equal(hash, expectedAssets[asset], name + ':' + asset); served[name + '/' + asset] = hash;
    })().catch(e => errors.push(e.message))); });
  }
  const { alice, phone, outside } = pages;
  const raw = fs.readFileSync(path.join(world, 'link'), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url')), invite = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
  delete invite.cert; offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(invite)).toString('base64url');
  await phone.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
  await phone.locator('#join-name').fill('phone'); await phone.locator('button.join-go').click();
  let request;
  await until('browser link', () => { request = cli('alice', 'person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*phone/m)?.[1]; return !!request; });
  cli('alice', 'person', 'approve', request); await ready(phone);
  await alice.goto(nativeURL('alice')); await ready(alice);
  await outside.goto(nativeURL('outside')); await ready(outside);
  stage = 'create synthetic named executor visibly';
  await outside.locator('#profile-btn').click(); await outside.getByRole('tab', { name: 'Agent', exact: true }).click();
  await outside.getByRole('button', { name: 'Create agent…', exact: true }).click();
  await outside.locator('#named-label').fill('Synthetic External Builder'); await outside.locator('#named-harness').selectOption('codex');
  await outside.locator('#named-dir').fill(path.join(world, 'work')); await outside.locator('#dialog-ok').click(); await outside.locator('#dialog').waitFor({ state: 'hidden' });
  const local = await outside.evaluate(() => agentnet.api('/api/agents'));
  const agentID = local.agents[0].record.id; assert.equal(local.agents[0].record.host, 'outside/host');
  await outside.locator('#settings-close').click(); assert.equal(harnessRuns().length, 0, 'configuration must not execute');
  const aliceAddress = await alice.evaluate(() => state.overview.me.address);
  for (const width of [390, 1280]) {
    for (const p of Object.values(pages)) await p.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    stage = 'seed synthetic selected/unselected history ' + width;
    const selectedBytes = 'SELECTED_HISTORY_EXACT_BYTES_' + width + '\n', excludedBytes = 'EXCLUDED_PRIVATE_FILE_' + width + '\n';
    const selectedPath = path.join(world, 'selected-' + width + '.txt'), excludedPath = path.join(world, 'excluded-' + width + '.txt');
    fs.writeFileSync(selectedPath, selectedBytes); fs.writeFileSync(excludedPath, excludedBytes);
    const conv = cli('bob', 'dm', 'new', aliceAddress).trim();
    cli('bob', 'dm', 'send', '--file', selectedPath, conv, 'Chosen context ' + width);
    cli('bob', 'dm', 'send', '--file', excludedPath, conv, 'Unselected private history ' + width);
    await openDM(alice, conv); await openDM(phone, conv);
    stage = 'visible exact external invite ' + width;
    await alice.locator('#agents').getByRole('button', { name: 'Invite an agent…', exact: true }).click();
    await alice.locator('input[id="agent-host:outside/host"]').check();
    await alice.locator('#invite-agent').selectOption(agentID);
    const chosen = alice.locator('#dialog label.choice').filter({ hasText: 'Chosen context ' + width });
    await chosen.locator('input').check();
    assert.equal(await alice.locator('#dialog label.choice').filter({ hasText: 'Unselected private history ' + width }).locator('input').isChecked(), false);
    assert((await alice.locator('#dialog').innerText()).includes('selected-' + width + '.txt'));
    await capture(alice, 'invite-selected-files', width);
    await alice.locator('#dialog-ok').click(); await alice.locator('#dialog').waitFor({ state: 'hidden' });
    const member = await readDM(alice, conv), participation = member.agents.find(a => a.agent_id === agentID && a.state === 'invited');
    assert(participation?.external); const pid = participation.pid;
    await openDM(outside, conv);
    const visitor = await readDM(outside, conv); assert.equal(visitor.role, 'visitor');
    assert(visitor.messages.some(m => m.body === 'Chosen context ' + width && m.excerpt_pid === pid && m.claimed_key));
    assert(!visitor.messages.some(m => m.body.includes('Unselected private history')));
    assert.equal(await outside.locator('#composer').isHidden(), true);
    assert.equal(await outside.locator('#agents').getByRole('button', { name: 'Invite an agent…', exact: true }).count(), 0);
    assert.equal(await outside.locator('#timeline').getByRole('button', { name: 'Reply', exact: true }).count(), 0);
    assert.equal(await outside.locator('#timeline [aria-label="Add a reaction"]').count(), 0);
    await outside.locator('#timeline details').first().getByText('Details', { exact: true }).click();
    await capture(outside, 'visitor-claimed-context', width);
    await outside.locator('#timeline details[open]').first().scrollIntoViewIfNeeded();
    await capture(outside, 'visitor-claimed-details', width);
    stage = 'visible outside owner acceptance ' + width;
    await outside.locator('#agents .agent-card.invited').getByRole('button', { name: 'Accept…', exact: true }).click();
    await capture(outside, 'owner-accept-selected-files', width);
    await outside.locator('#dialog-ok').click(); await outside.locator('#dialog').waitFor({ state: 'hidden' });
    for (const p of [alice, phone]) await p.waitForFunction(pid => state.dmData.agents.some(a => a.pid === pid && a.can_ask), pid);
    const runBaseline = harnessRuns().length;
    stage = 'native file-only question ' + width;
    await alice.locator('#agents .agent-card.active').getByRole('button', { name: 'Ask', exact: true }).click();
    const currentBytes = Buffer.from('CURRENT_NATIVE_EXACT_BYTES_' + width + '\n');
    await alice.locator('#file-input').setInputFiles({ name: 'native-current-' + width + '.txt', mimeType: 'text/plain', buffer: currentBytes });
    await capture(alice, 'native-file-only-question', width); await alice.locator('#send').click();
    await until('real synthetic native report', () => harnessRuns().length === runBaseline + 1);
    const first = harnessRuns().at(-1).prompt;
    assert(first.includes(selectedBytes)); assert(first.includes(currentBytes.toString())); assert(!first.includes(excludedBytes)); assert(!first.includes('Unselected private history'));
    const nativeFiles = harnessRuns().at(-1).files;
    assert.deepEqual(nativeFiles.find(f => f.name === 'native-current-' + width + '.txt').bytes, [...currentBytes]);
    assert.deepEqual(nativeFiles.find(f => f.name === 'selected-' + width + '.txt').bytes, [...Buffer.from(selectedBytes)]);
    assert(!nativeFiles.some(f => f.name.startsWith('excluded-')));
    for (const p of [alice, phone, outside]) await p.waitForFunction(pid => state.dmData.messages.some(m => m.pid === pid && m.kind === 'answer'), pid);
    assert.equal(await phone.locator('#timeline .replyref').filter({ hasText: 'Reply to a message not shown here' }).count(), 0, 'linked audience report has its exact visible request');
    for (const p of [alice, phone, outside]) {
      const d = await readDM(p, conv);
      const nativeRequest = d.messages.find(m => m.pid === pid && m.kind === 'question' && (m.attachments || []).some(f => f.name === 'native-current-' + width + '.txt'));
      assert(nativeRequest && !nativeRequest.agent_id, 'executing a human request never stamps executor as author');
      const row = p.locator('[id="m-' + nativeRequest.id + '"]');
      assert(!(await row.locator('.meta .who').innerText()).includes('Agent'), 'executed question renders human author');
      if (p === outside) { assert.equal(await row.locator('.meta .who').innerText(), nativeRequest.from, 'visitor uses exact supplied question sender'); await row.scrollIntoViewIfNeeded(); await capture(p, 'executed-human-question', width); }
    }
    stage = 'browser file-only task + explicit host acceptance ' + width;
    await phone.locator('#agents .agent-card.active').getByRole('button', { name: 'Ask', exact: true }).click();
    await phone.getByRole('radio', { name: 'Task', exact: true }).check();
    const browserBytes = Buffer.from('CURRENT_BROWSER_EXACT_BYTES_' + width + '\n');
    await phone.locator('#file-input').setInputFiles({ name: 'browser-current-' + width + '.txt', mimeType: 'text/plain', buffer: browserBytes });
    await capture(phone, 'browser-file-only-task', width); await phone.locator('#send').click();
    await outside.locator('#timeline').getByRole('button', { name: 'Accept and run…', exact: true }).waitFor();
    assert.equal(harnessRuns().length, runBaseline + 1, 'task must await owner acceptance');
    await outside.locator('#timeline').getByRole('button', { name: 'Accept and run…', exact: true }).click();
    assert((await outside.locator('#dialog').innerText()).includes('using its local configuration on that computer'));
    assert(!(await outside.locator('#dialog').innerText()).includes('no responder is set'));
    await outside.locator('#gate').check();
    await capture(outside, 'named-task-acceptance', width);
    await outside.locator('#dialog-ok').click(); await outside.locator('#dialog').waitFor({ state: 'hidden' });
    await until('real synthetic browser task report', () => harnessRuns().length === runBaseline + 2);
    const second = harnessRuns().at(-1).prompt; assert(second.includes(browserBytes.toString())); assert(second.includes(selectedBytes)); assert(!second.includes(excludedBytes));
    assert.deepEqual(harnessRuns().at(-1).files.find(f => f.name === 'browser-current-' + width + '.txt').bytes, [...browserBytes]);
    assert(!harnessRuns().at(-1).files.some(f => f.name.startsWith('excluded-')));
    for (const p of [alice, phone]) await p.waitForFunction(pid => state.dmData.messages.filter(m => m.pid === pid && (m.kind === 'answer' || m.kind === 'result')).length >= 2, pid);
    const taskView = await readDM(outside, conv), humanTask = taskView.messages.find(m => m.pid === pid && m.kind === 'task');
    assert(humanTask && !humanTask.agent_id, 'executed task retains human author');
    const taskRow = outside.locator('[id="m-' + humanTask.id + '"]');
    assert(!(await taskRow.locator('.meta .who').innerText()).includes('Agent'), 'executed task renders human author');
    assert.equal(await taskRow.locator('.meta .who').innerText(), humanTask.from, 'visitor uses exact supplied task sender');
    await taskRow.scrollIntoViewIfNeeded(); await capture(outside, 'executed-human-task', width);
    if (authorshipOnly) { checks.push({ width, conv, pid, agentID, visitorHumanQuestion: true, visitorHumanTask: true, questionSender: 'admin/laptop', taskSender: 'admin/phone', exactDecryptedBytes: true, namedApprovalAccurate: true, runs: 2 }); continue; }
    stage = 'visible browser follow-up ' + width;
    await phone.getByRole('radio', { name: 'Question', exact: true }).check();
    await phone.locator('#body').fill('Follow up real encrypted ' + width); await phone.locator('#send').click();
    await until('follow-up execution', () => harnessRuns().length === runBaseline + 3);
    for (const p of [alice, phone]) {
      await p.waitForFunction(pid => state.dmData.messages.filter(m => m.pid === pid && (m.kind === 'answer' || m.kind === 'result')).length >= 3, pid);
      await p.locator('#replying-cancel').click();
      await p.locator('#timeline li.msg').filter({ hasText: 'Synthetic external report: selected context and current files checked.' }).last().scrollIntoViewIfNeeded();
      await capture(p, p === alice ? 'native-reports' : 'browser-reports', width);
      await p.locator('#agents .agent-card.active').getByRole('button', { name: 'Ask', exact: true }).click();
    }
    const final = await readDM(phone, conv);
    const requests = final.messages.filter(m => m.pid === pid && ['question', 'task'].includes(m.kind));
    assert(requests.some(m => m.kind === 'question' && !m.body && m.attachments.some(f => f.name === 'native-current-' + width + '.txt')));
    assert(requests.some(m => m.kind === 'task' && !m.body && m.attachments.some(f => f.name === 'browser-current-' + width + '.txt')));
    assert(final.messages.filter(m => m.pid === pid && ['answer', 'result'].includes(m.kind)).every(m => m.agent_id === agentID && m.from === 'outside/host'));
    stage = 'visible dismissal ' + width;
    await phone.locator('#agents .agent-card.active').getByRole('button', { name: 'Dismiss…', exact: true }).click(); await phone.locator('#dialog-ok').click(); await phone.locator('#dialog').waitFor({ state: 'hidden' });
    assert.equal(await phone.locator('#send').isDisabled(), true);
    await outside.waitForFunction(pid => state.dmData.agents.some(a => a.pid === pid && a.state === 'dismissed'), pid);
    assert.equal(harnessRuns().length, runBaseline + 3);
    checks.push({ width, conv, pid, agentID, selectedFilesOnly: true, exactNativeFileOnlyQuestion: true, exactBrowserFileOnlyTask: true, hostAcceptedTask: true, bothProvidersThreeReports: true, followUpDismiss: true, visitorRestrictions: true, runs: 3, currentFileSHA256: [digest(currentBytes), digest(browserBytes)] });
  }
  await Promise.all(assetReads); assert.deepEqual(errors, []); assert.deepEqual(external, []);
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, kind: authorshipOnly ? 'affected real visitor authorship UI gate with synthetic executor only' : 'real encrypted loopback with synthetic executor only', checks, shots, expectedAssets, served, errors, external, world }, null, 2), { mode: 0o600 });
  console.log(authorshipOnly ? 'PASS affected real visitor authorship UI, 390/1280, four synthetic runs' : 'PASS real external-agent UI lifecycle, native/browser, 390/1280, six synthetic runs');
})().catch(async e => {
  console.error('FAIL stage: ' + stage); console.error(e);
  fs.writeFileSync(path.join(evidence, 'failure.json'), JSON.stringify({ stage, error: e.stack, errors, external, world }, null, 2), { mode: 0o600 });
  if (browser) for (const context of browser.contexts()) for (const p of context.pages()) {
    const name = new URL(p.url()).origin === origin ? 'phone' : new URL(p.url()).origin === new URL(nativeURL('outside')).origin ? 'outside' : 'alice';
    await p.screenshot({ path: path.join(evidence, 'failure-' + name + '.png') });
    fs.writeFileSync(path.join(evidence, 'failure-' + name + '.txt'), await p.locator('body').innerText(), { mode: 0o600 });
  }
  process.exitCode = 1;
}).finally(async () => { if (browser) await browser.close(); });
