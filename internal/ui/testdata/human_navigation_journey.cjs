// Real linked Engine/IndexedDB + native daemon UI. User actions only click
// visible controls; CLI creates the populated test history and approves linking.
const fs = require('node:fs'), path = require('node:path'), assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { X509Certificate, createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const world = process.env.AGENTNET_HUMAN_WORLD, evidence = process.env.AGENTNET_SCREENSHOTS;
assert(world && evidence && process.env.AGENTNET_HUMAN_BINARY, 'explicit private fixture paths required');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const origin = 'https://127.0.0.1:' + fs.readFileSync(path.join(world, 'mux-port'), 'utf8').trim();
const nativeURL = fs.readFileSync(path.join(world, 'alice-ui'), 'utf8').match(/http:\/\/127\.0\.0\.1:\d+\/\?t=[a-f0-9]+/)[0];
const allowed = new Set([origin, new URL(nativeURL).origin]);
const cli = (name, ...args) => execFileSync(process.env.AGENTNET_HUMAN_BINARY, ['--home', path.join(world, name), ...args], { encoding: 'utf8', env: process.env });
const cert = new X509Certificate(fs.readFileSync(path.join(world, 'cert.pem')));
const spki = createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const assetNames = ['app.js', 'app.css', 'lenses.js', 'engine.mjs', 'wire.mjs', 'device.mjs'];
const sourceHashes = process.env.AGENTNET_HUMAN_ASSET_MANIFEST
  ? Object.fromEntries(fs.readFileSync(process.env.AGENTNET_HUMAN_ASSET_MANIFEST, 'utf8').trim().split('\n').map(line => {
    const [hash, file] = line.trim().split(/\s+/); return [path.basename(file), hash];
  }))
  : Object.fromEntries(assetNames.map(name => [name, digest(fs.readFileSync(path.join('internal/ui/static', name)))]));
// A coordinator may explicitly bind Engine to a previously tested snapshot
// while another owner finishes unrelated shared-source work. Never imply parity.
const expectedAssets = { ...sourceHashes, ...(process.env.AGENTNET_HUMAN_ENGINE_SHA256 ? { 'engine.mjs': process.env.AGENTNET_HUMAN_ENGINE_SHA256 } : {}) };
const errors = [], external = [], checks = [], shots = [], paths = [], assetReads = [], servedAssets = {};
const topics = [], ownThreads = [];
const widths = (process.env.AGENTNET_HUMAN_WIDTHS || '390,1280').split(',').map(Number);
assert(widths.every(w => w === 390 || w === 1280), 'only the assigned viewports');
const bobLabel = 'Bob Collaboration With An Exceptionally Long Display Name';
const idle = ms => new Promise(resolve => setTimeout(resolve, ms));
let browser, stage = 'link setup';
const ready = page => page.waitForFunction(() => typeof state !== 'undefined' && state.overview, null, { timeout: 20000 });
async function until(label, predicate, timeout = 20000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) { if (await predicate()) return; await idle(100); }
  throw Error('Timed out: ' + label);
}
async function click(page, locator, label, route) {
  await locator.click();
  if (route) route.push(label);
}
async function home(page, route) {
  if (await page.locator('#settings').isVisible()) await click(page, page.locator('#settings-close'), 'Close settings', route);
  for (let up = 0; up < 3 && !(await page.locator('#nav-chats').isVisible()); up++) {
    await click(page, page.locator('#back'), 'Back to person/device, then home', route);
  }
  await click(page, page.locator('#nav-chats'), 'Chats (home)', route);
  await page.locator('#search:visible, #zoom .zoom-search:visible').first().waitFor({ state: 'visible' });
}
async function capture(page, name, width) {
  // Zoom's finite 0.5s transition still renders the outgoing layer after data
  // is ready. Capture the settled view, not an intermediate animation frame.
  await page.waitForFunction(() => !document.querySelector('#zoom .zoom-arrive-in, #zoom .zoom-arrive-out'));
  const sizes = await page.evaluate(() => ({ document: document.documentElement.scrollWidth > innerWidth,
    dialogs: [...document.querySelectorAll('dialog[open]')].map(n => n.scrollWidth > n.clientWidth + 1) }));
  const file = path.join(evidence, `human-navigation-${name}-${width}.png`);
  await page.screenshot({ path: file }); shots.push(file);
  assert.equal(sizes.document, false, name + ': horizontal document overflow');
  assert(sizes.dialogs.every(x => !x), name + ': horizontal dialog overflow');
}
async function device(page, address, width, route, captureName) {
  await home(page, route);
  await click(page, page.locator('#profile-btn'), 'You / Profile', route);
  await page.locator('#profile-card').getByText('One person, 2 devices', { exact: true }).waitFor();
  await click(page, page.locator('#profile-devices summary').filter({ hasText: 'You on 2 devices' }), 'You on 2 devices', route);
  const rows = page.locator('#profile-devices .device-row');
  assert.equal(await rows.count(), 2);
  assert.equal(await rows.filter({ hasText: '(this device)' }).count(), 1);
  if (captureName) await capture(page, captureName, width);
  return rows.filter({ hasText: address });
}
async function startDeviceMessage(page, address, body, width, name) {
  const route = [];
  const row = await device(page, address, width, route, name + '-devices');
  const write = row.getByRole('button', { name: 'Write to it…', exact: true });
  if (await write.count()) await click(page, write, address + ': Write to it…', route);
  else {
    await click(page, row.getByRole('button'), address + ': device conversations', route);
    await click(page, page.locator('#hub').getByRole('button', { name: 'New conversation with ' + address, exact: true }), 'New device conversation', route);
  }
  assert.equal(await page.locator('#new-to').inputValue(), address);
  assert.equal(await page.locator('#new-kind').inputValue(), 'message');
  await page.locator('#new-body').fill(body);
  await click(page, page.locator('#dialog-ok'), 'Send plain message', route);
  await page.locator('#dialog').waitFor({ state: 'hidden' });
  if (await page.locator('#settings').isVisible()) await click(page, page.locator('#settings-close'), 'Close settings', route);
  await page.waitForFunction(body => state.data?.messages.some(m => m.body === body), body);
  const result = await page.evaluate(body => ({ id: state.data.messages.find(m => m.body === body).id, peer: state.data.peer, dm: state.dm }), body);
  assert.equal(result.peer, address); assert(!result.dm);
  paths.push({ width, platform: name, action: 'start', clicks: route.length, route, root: result.id });
  return result.id;
}
async function openDeviceMessage(page, address, body, width, name) {
  const route = [];
  const record = { width, platform: name, action: 'open for reply', route };
  paths.push(record);
  const row = await device(page, address, width, route, name + '-reply-discovery');
  await click(page, row.getByRole('button'), address + ': device conversations', route);
  const message = page.locator('#hub .thread-row').filter({ hasText: body });
  if (!(await message.count())) {
    await click(page, page.locator('#hub .singles-toggle'), 'Show single messages', route);
    await page.waitForFunction(body => [...document.querySelectorAll('#hub .thread-row')].some(n => n.textContent.includes(body)), body, { timeout: 2000 }).catch(async e => {
      record.toggleProof = await page.evaluate(address => ({ address, stateOpen: state.singlesOpen[address],
        displayedExpanded: document.querySelector('#hub .singles-toggle')?.getAttribute('aria-expanded'),
        displayedRows: document.querySelectorAll('#hub .thread-row').length, hub: state.hub,
        messagesReceived: state.overview.threads.filter(t => t.peer === address).map(t => ({ id: t.id, title: t.title, count: t.count })) }), address);
      throw Error('Single-message disclosure did not redraw: ' + JSON.stringify(record.toggleProof));
    });
    const toggle = page.locator('#hub .singles-toggle');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    await click(page, toggle, 'Collapse single messages', route);
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    assert.equal(await page.locator('#hub .thread-list.singles').count(), 0);
    await click(page, toggle, 'Expand single messages again', route);
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    assert(await message.count() > 0);
    record.toggleExpandCollapse = true;
  }
  await click(page, message, 'Open intended device message/topic', route);
  await page.waitForFunction(body => state.data?.messages.some(m => m.body === body), body);
  assert.equal(await page.evaluate(() => state.dm), null);
  record.clicks = route.length;
}
async function reply(page, body, root) {
  await page.locator('#body').fill(body);
  await page.locator('#send').click();
  await page.waitForFunction(body => state.data?.messages.some(m => m.body === body), body);
  return page.evaluate(({ body, root }) => {
    const messages = state.data.messages;
    const first = messages.find(m => m.id === root), response = messages.find(m => m.body === body);
    return { root: first.id, reply: response.id, reply_to: response.reply_to, count: messages.length, peer: state.data.peer };
  }, { body, root });
}
const dmSnapshot = (page, id) => page.evaluate(async id => {
  const d = await agentnet.api('/api/dm?id=' + id);
  return { id: d.id, person: d.peer.person, messages: d.messages.map(m => ({ id: m.id, from: m.from, body: m.body, kind: m.kind, reply_to: m.reply_to || '' })) };
}, id);
async function appearance(page, lens) {
  await home(page);
  await page.locator('#profile-btn').click();
  await page.getByRole('tab', { name: 'Appearance', exact: true }).click();
  await page.locator('#lens').getByRole('button', { name: lens, exact: true }).click();
  await page.locator('#settings-close').click();
  await page.waitForFunction(lens => state.lens === lens, lens.toLowerCase());
}
async function openTopicBySearch(page, topic, lens) {
  const search = lens === 'Zoom' ? page.locator('#zoom .zoom-layer').last().locator('.zoom-search') : page.locator('#search');
  await search.fill(topic.prefix);
  const root = lens === 'Zoom' ? page.locator('#zoom .zoom-layer').last() : page.locator('#conv-list');
  await root.locator('button.result').filter({ hasText: topic.prefix }).click();
  await page.waitForFunction(id => state.dm === id && state.dmData?.id === id, topic.id);
  if (lens === 'Comic') {
    const start = page.locator('#comic').getByRole('button', { name: 'Start reading', exact: true });
    if (await start.isVisible()) await start.click();
  }
}
async function groupedTopics(page, width, platform, baseline) {
  await home(page);
  const side = page.locator('#conv-list');
  assert.equal(await side.locator('.contact-item').filter({ hasText: bobLabel }).count(), 1);
  assert.equal(await side.locator('.contact-item').filter({ hasText: 'Dana Design Operations' }).count(), 1);
  assert.equal(await side.locator('.thread-title').count(), 0, 'topics stay beneath one person, not sidebar rows');
  const rows = side.locator('.contact-item').filter({ hasText: bobLabel });
  await rows.getByText('3 chats · 1 device', { exact: true }).waitFor();
  if (platform === 'native') await capture(page, 'populated-home', width);
  await rows.locator('button.contact').click();
  await page.locator('#hub .thread-row').first().waitFor();
  assert.equal(await page.locator('#hub .thread-row').count(), 3);
  assert((await page.locator('#conv-topic').innerText()).includes('each DM is a separate conversation'));
  if (platform === 'native' && width === 1280) {
    assert.equal(await page.locator('#hub .thread-row .unread').count(), 3, 'three independent unread Bob topics');
    await capture(page, 'unread-person-topics', width);
  }
  if (platform === 'phone') await capture(page, 'person-topics', width);
  for (const topic of topics.filter(t => t.account === 'bob')) {
    await page.locator('#hub .thread-row').filter({ hasText: topic.prefix }).click();
    await page.waitForFunction(id => state.dm === id && state.dmData?.id === id, topic.id);
    assert.equal(await page.locator('#conv-name').innerText(), bobLabel);
    assert((await page.locator('#conv-topic').innerText()).includes('DM with a person'));
    assert.deepEqual(await dmSnapshot(page, topic.id), baseline.get(topic.id));
    const back = width === 390 ? page.locator('#back') : page.locator('#hub-back');
    await back.click();
    await page.locator('#hub .thread-row').first().waitFor();
    assert.equal(await page.locator('#hub .thread-row').count(), 3);
  }
  // Search opens the exact second topic through the same visible result path.
  await home(page);
  await page.locator('#search').fill(topics[1].prefix);
  const searchResult = side.locator('button.result').filter({ hasText: topics[1].prefix });
  await searchResult.waitFor();
  if (platform === 'phone' && width === 390) await capture(page, 'topic-search', width);
  await searchResult.click();
  await page.waitForFunction(id => state.dm === id && state.dmData?.id === id, topics[1].id);
  // Dana's independent topics belong to Dana, never to Bob's person hub.
  await home(page);
  await side.locator('.contact-item').filter({ hasText: 'Dana Design Operations' }).locator('button.contact').click();
  await page.locator('#hub .thread-row').first().waitFor();
  assert.equal(await page.locator('#hub .thread-row').count(), 2);
  for (const topic of topics.filter(t => t.account === 'dana')) {
    const row = page.locator('#hub .thread-row').filter({ hasText: topic.prefix });
    assert.equal(await row.count(), 1);
    await row.click();
    await page.waitForFunction(id => state.dm === id && state.dmData?.id === id, topic.id);
    assert.equal(await page.locator('#conv-name').innerText(), 'Dana Design Operations');
    assert.deepEqual(await dmSnapshot(page, topic.id), baseline.get(topic.id));
    await (width === 390 ? page.locator('#back') : page.locator('#hub-back')).click();
    await page.locator('#hub .thread-row').first().waitFor();
  }
  if (platform === 'phone') await capture(page, 'dana-person-topics', width);
  await home(page);
  const service = side.locator('.contact-item').filter({ hasText: 'service/desk' });
  assert.equal(await service.count(), 1);
  assert.equal(await service.locator('.person-name').count(), 0);
  await service.locator('button.contact').click();
  await page.waitForFunction(() => state.data?.peer === 'service/desk');
  assert.equal(await page.evaluate(() => state.dm), null);
  if (platform === 'native') await capture(page, 'standalone-service', width);
  for (const lens of ['Classic', 'Comic', 'Zoom']) {
    await appearance(page, lens);
    await openTopicBySearch(page, topics[1], lens);
    await page.waitForFunction(id => state.dm === id && state.dmData?.id === id, topics[1].id);
    const author = lens === 'Classic' ? page.locator('#timeline .who') : lens === 'Comic' ? page.locator('#comic .panel-foot') : page.locator('#zoom .mc-who');
    await author.filter({ hasText: bobLabel }).first().waitFor();
    assert.deepEqual(await dmSnapshot(page, topics[1].id), baseline.get(topics[1].id));
    if (platform === 'phone') {
      await capture(page, 'topic-' + lens.toLowerCase(), width);
      if (lens === 'Comic' && width === 390) {
        await author.last().scrollIntoViewIfNeeded();
        await capture(page, 'topic-comic-last-panel', width);
      }
    }
  }
  // Zoom returns to this person; its level-one rows retain distinct IDs too.
  await page.locator('#zoom .ladder button').nth(1).click();
  await page.locator('#zoom .zoom-person .thread-row').first().waitFor();
  assert.equal(await page.locator('#zoom .zoom-person .thread-row').count(), 3);
  await page.locator('#zoom .zoom-person .thread-row').filter({ hasText: topics[0].prefix }).click();
  await page.waitForFunction(id => state.dm === id && state.dmData?.id === id, topics[0].id);
  assert.deepEqual(await dmSnapshot(page, topics[0].id), baseline.get(topics[0].id));
  if (width === 1280) await appearance(page, 'Classic');
  checks.push({ width, platform, groupedPerson: true, separateTopics: true, searchExactID: true, backPreservesTopics: true, lensAuthorsAndHistory: true, standaloneServiceDistinct: true });
}
(async () => {
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM, args: ['--ignore-certificate-errors-spki-list=' + spki] });
  const contexts = [];
  for (let i = 0; i < 2; i++) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } }); contexts.push(context);
    await context.route('**/*', route => {
      if (allowed.has(new URL(route.request().url()).origin)) return route.continue();
      external.push(new URL(route.request().url()).origin); return route.abort();
    });
  }
  const phone = await contexts[0].newPage(), native = await contexts[1].newPage();
  for (const [platform, page] of [['phone', phone], ['native', native]]) {
    page.setDefaultTimeout(15000);
    page.on('pageerror', e => errors.push(e.stack || e.message));
    page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
    page.on('response', response => {
      const name = new URL(response.url()).pathname.split('/').pop();
      if (assetNames.includes(name)) assetReads.push((async () => {
        const value = digest(await response.body());
        assert.equal(value, expectedAssets[name], platform + ': source/binary mismatch ' + name);
        servedAssets[platform + '/' + name] = value;
      })().catch(e => errors.push('asset verification: ' + e.message)));
    });
  }
  const raw = fs.readFileSync(path.join(world, 'link'), 'utf8').match(/agentnet-link-v2:[A-Za-z0-9_-]+/)[0];
  const offer = JSON.parse(Buffer.from(raw.split(':')[1], 'base64url'));
  const invite = JSON.parse(Buffer.from(offer.invite.split(':')[1], 'base64url'));
  delete invite.cert;
  offer.invite = 'agentnet-invite-v1:' + Buffer.from(JSON.stringify(invite)).toString('base64url');
  await phone.goto(origin + '/#agentnet-link-v2:' + Buffer.from(JSON.stringify(offer)).toString('base64url'));
  await phone.locator('#join-name').fill('phone'); await phone.locator('button.join-go').click();
  let request;
  await until('actual device-link request', async () => {
    request = cli('alice', 'person', 'links').match(/^([a-f0-9]{32})\s+pending\s+[^\n]*phone/m)?.[1];
    return !!request;
  });
  cli('alice', 'person', 'approve', request);
  await ready(phone); await phone.waitForFunction(() => state.overview.device.online && state.overview.directory.current);
  await native.goto(nativeURL); await ready(native);
  await native.waitForFunction(() => state.overview.person?.devices.length === 2);
  await Promise.all(assetReads);
  for (const name of assetNames) assert(servedAssets['phone/' + name], 'served browser asset missing: ' + name);
  for (const name of ['app.js', 'app.css', 'lenses.js']) assert(servedAssets['native/' + name], 'served native asset missing: ' + name);
  const identity = await Promise.all([native, phone].map(p => p.evaluate(() => ({ person: state.overview.person.person, address: state.overview.me.address, devices: state.overview.person.devices.map(d => d.address).sort() }))));
  assert.equal(identity[0].person, identity[1].person); assert.deepEqual(identity[0].devices, identity[1].devices);
  assert(identity[0].address.endsWith('/laptop')); assert(identity[1].address.endsWith('/phone'));
  const laptopAddress = identity[0].address, phoneAddress = identity[1].address;
  for (const width of widths) {
    stage = 'own-device messaging ' + width;
    for (const page of [phone, native]) await page.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    const laptopBody = 'Laptop to phone plain ' + width, phoneReply = 'Phone reply in laptop topic ' + width;
    const laptopRoot = await startDeviceMessage(native, phoneAddress, laptopBody, width, 'native');
    await phone.waitForFunction(body => state.overview.threads.some(t => t.title.includes(body)), laptopBody);
    await openDeviceMessage(phone, laptopAddress, laptopBody, width, 'phone');
    const answered = await reply(phone, phoneReply, laptopRoot);
    assert.equal(answered.reply_to, laptopRoot);
    await native.waitForFunction(body => state.data?.messages.some(m => m.body === body), phoneReply);
    const phoneBody = 'Phone to laptop plain ' + width, laptopReply = 'Laptop reply in phone topic ' + width;
    const phoneRoot = await startDeviceMessage(phone, laptopAddress, phoneBody, width, 'phone');
    await native.waitForFunction(body => state.overview.threads.some(t => t.title.includes(body)), phoneBody);
    await openDeviceMessage(native, phoneAddress, phoneBody, width, 'native');
    const reversed = await reply(native, laptopReply, phoneRoot);
    assert.equal(reversed.reply_to, phoneRoot);
    await phone.waitForFunction(body => state.data?.messages.some(m => m.body === body), laptopReply);
    await capture(phone, 'own-device-conversation', width);
    ownThreads.push({ width, laptopRoot, phoneRoot, phoneReply: answered.reply, laptopReply: reversed.reply, distinctDeviceThreads: laptopRoot !== phoneRoot });
    checks.push({ width, bothDirections: true, repliesInExpectedDeviceThreads: true, onePersonTwoDevices: true });
  }
  stage = 'populated topics setup';
  for (const page of [native, phone]) await home(page);
  for (const [account, prefix, long] of [['bob', 'Release planning topic', 'QuarterlyRoadmap'.repeat(5)], ['bob', 'Access review topic', 'PermissionsAndKeys'.repeat(5)], ['bob', 'Support handoff topic', 'OpenQuestions'.repeat(5)], ['dana', 'Design review topic', 'MobileReadability'.repeat(4)], ['dana', 'Operations topic', 'RolloutWindows'.repeat(4)]]) {
    const id = cli(account, 'dm', 'new', laptopAddress).trim();
    cli(account, 'dm', 'send', id, prefix + ' ' + long);
    cli(account, 'dm', 'send', id, prefix + ' independent second message');
    topics.push({ account, prefix, id });
  }
  // Device-thread delivery targets one exact address, unlike person DM fan-out.
  for (const address of [laptopAddress, phoneAddress]) {
    cli('service', 'send', address, 'Standalone deploy notifier remains a service');
  }
  for (const page of [native, phone]) await page.waitForFunction(ids => ids.every(id => (state.overview.dms || []).some(d => d.id === id && d.count === 2 && d.unread > 0)) && (state.overview.threads || []).some(t => t.peer === 'service/desk'), topics.map(t => t.id));
  // Fan-out uses distinct envelope IDs for each recipient (conv.go SendConv).
  // Keep IDs stable within each provider; compare semantic content across them.
  const baselines = new Map();
  for (const [platform, page] of [['native', native], ['phone', phone]]) {
    baselines.set(platform, new Map(await Promise.all(topics.map(async t => [t.id, await dmSnapshot(page, t.id)]))));
  }
  const semantic = snapshot => ({ ...snapshot, messages: snapshot.messages.map(({ id, ...message }) => message) });
  for (const topic of topics) assert.deepEqual(semantic(baselines.get('phone').get(topic.id)), semantic(baselines.get('native').get(topic.id)));
  assert.equal(new Set(topics.map(t => t.id)).size, 5);
  const unread = await Promise.all([native, phone].map(p => p.evaluate(() => state.overview.dms.map(d => ({ id: d.id, unread: d.unread })))));
  for (const width of [...widths].sort((a, b) => b - a)) {
    for (const [platform, page] of [['native', native], ['phone', phone]]) {
      stage = platform + ' grouped navigation ' + width;
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 });
      await groupedTopics(page, width, platform, baselines.get(platform));
    }
  }
  for (const [platform, page] of [['native', native], ['phone', phone]]) for (const topic of topics) assert.deepEqual(await dmSnapshot(page, topic.id), baselines.get(platform).get(topic.id));
  await phone.reload(); await ready(phone);
  assert.equal(await phone.evaluate(() => agentnet.platform), 'browser');
  assert.equal(await phone.evaluate(() => state.overview.person.person), identity[0].person);
  for (const topic of topics) assert.deepEqual(await dmSnapshot(phone, topic.id), baselines.get('phone').get(topic.id));
  await Promise.all(assetReads); assert.deepEqual(errors, []); assert.deepEqual(external, []);
  fs.writeFileSync(path.join(evidence, 'result.json'), JSON.stringify({ pass: true, world, identity, paths, ownThreads, topics, unread, checks, shots, servedAssets, sourceHashes, errors, external, actualIndexedDBReload: true,
    binarySHA256: digest(fs.readFileSync(process.env.AGENTNET_HUMAN_BINARY)) }, null, 2), { mode: 0o600 });
  console.log('PASS actual native/linked-browser human navigation: own-device messages/replies, one person, grouped distinct topics, unread/search/back, Classic/Comic/Zoom, service separation, 390/1280');
})().catch(async e => {
  console.error(stage + ': ' + e.message);
  for (const record of paths) record.clicks = record.route.length;
  fs.writeFileSync(path.join(evidence, 'failure.json'), JSON.stringify({ pass: false, stage, error: e.message, world, paths, ownThreads, topics, checks, shots, servedAssets, errors, external }, null, 2), { mode: 0o600 });
  if (browser) for (const [i, context] of browser.contexts().entries()) {
    const page = context.pages()[0];
    if (page) { await page.screenshot({ path: path.join(evidence, 'failure-' + i + '.png') });
      console.error(JSON.stringify({ view: i, text: (await page.locator('body').innerText()).slice(0, 2200) })); }
  }
  process.exitCode = 1;
}).finally(async () => { if (browser) await browser.close(); });
