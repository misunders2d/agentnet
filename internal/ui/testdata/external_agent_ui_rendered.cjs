// Isolated production UI over inert provider responses. No real harness,
// crypto/transport or execution claim; full loopback integration is separate.
const assert = require('node:assert/strict'), fs = require('node:fs'), path = require('node:path');
const http = require('node:http'), { createHash } = require('node:crypto');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const root = path.resolve(__dirname, '../static');
const evidence = process.env.AGENTNET_SCREENSHOTS;
assert(evidence, 'explicit private evidence directory required');
fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const assetNames = ['app.js', 'app.css', 'lenses.js'];
const assets = Object.fromEntries(assetNames.map(n => [n, fs.readFileSync(path.join(root, n))]));
const digest = b => createHash('sha256').update(b).digest('hex');
const hashes = Object.fromEntries(assetNames.map(n => [n, digest(assets[n])]));
const markup = fs.readFileSync(path.join(root, 'default.html'), 'utf8');
const agentID = 'c'.repeat(32), at = '2026-10-01T10:00:00Z';
const me = { person: 'person-me', label: 'Me', address: 'me/laptop', fingerprint: 'me-key', state: 'self', devices: [] };
const peer = { person: 'person-alice', label: 'Alice Human', address: 'alice/laptop', fingerprint: 'alice-key', state: 'checked', devices: [] };
function bootstrap() {
  const browser = new URL(location.href).searchParams.get('mode') === 'browser';
  const seed = SEED;
  const overview = { version: 'fixture', me: { address: seed.me.address, fingerprint: seed.me.fingerprint, browser }, person: seed.me,
    persons: true, agents: true, files: { max_file: 1024 * 1024, max_count: 4 }, threads: [], review: [], quarantine: [], seq: 1,
    directory: { status: 'listed', current: true, at: seed.at, members: [{ address: 'outside/host', presence: 'connected' }] },
    people: [seed.peer], dms: [{ id: 'room', peer: seed.peer, created: seed.at, last_at: seed.at, count: 2, unread: 0 }] };
  const thread = { id: 'room', role: 'member', peer: seed.peer, created: seed.at, messages: [
    { id: 'chosen', dir: 'in', from: seed.peer.address, body: 'Chosen earlier context', kind: 'message', origin: 'ui', at: seed.at, attachments: [{ name: 'selected-history.txt', size: 7, openable: false, note: 'Fixture metadata only' }], can: ['react'] },
    { id: 'excluded', dir: 'in', from: seed.peer.address, body: 'Unselected private history', kind: 'message', origin: 'ui', at: seed.at, attachments: [{ name: 'excluded-history.txt', size: 9, openable: false }] }], agents: [] };
  const staged = new Map(); let next = 0;
  window.fixture = { requests: [], failAsk: false, overview, thread };
  const provider = { platform: browser ? 'browser' : 'daemon', workspace: { id: 'fixture-workspace' }, skins: [], onOpen: () => {}, listen: changed => { window.fixture.change = changed; return () => {}; },
    stage: async file => { if (browser) return { workspace: 'fixture-workspace', name: file.name, size: file.size, arrayBuffer: () => file.arrayBuffer() }; const id = 'stage-' + ++next; staged.set(id, [...new Uint8Array(await file.arrayBuffer())]); return id; },
    api: async (url, body) => {
      const u = new URL(url, location.origin), p = u.pathname;
      const request = { path: p, body: body ? JSON.parse(JSON.stringify(body)) : undefined };
      if (body) window.fixture.requests.push(request);
      if (p === '/api/overview') return structuredClone(overview);
      if (p === '/api/dm') return structuredClone(thread);
      if (p === '/api/agents') return { host: 'outside/host', agents: [{ enabled: true, record: { id: seed.agentID, host: 'outside/host', label: 'External Builder', host_key: 'host-key' } }] };
      if (p === '/api/dm/agent/invite') {
        thread.agents = [{ pid: 'outside-pid', external: true, agent_id: body.agent_id, host: { label: 'Outside Person', address: body.host }, inviter: seed.me, state: 'active', state_text: 'Fixture host accepted', shared: body.share, missing: 0, tasks_from: [], can_ask: true, can_dismiss: true }];
        return {};
      }
      if (p === '/api/dm/agent/ask') {
        if (browser && (body.files || []).some(f => f.workspace !== 'fixture-workspace')) throw Error('File belongs to another workspace');
        request.bytes = await Promise.all((body.files || []).map(async f => typeof f === 'string' ? staged.get(f) : [...new Uint8Array(await f.arrayBuffer())]));
        (body.files || []).filter(f => typeof f === 'string').forEach(f => staged.delete(f));
        if (window.fixture.failAsk) throw Error('Fixture host unavailable');
        thread.messages.push({ id: 'report-' + ++next, dir: 'in', from: 'outside/host', agent_id: seed.agentID, body: 'Fixture report for ' + (body.body || 'file-only request'), kind: 'answer', origin: 'agent:fixture', at: seed.at, pid: body.pid });
        setTimeout(() => window.fixture.change({ type: 'change', seq: ++overview.seq }), 0);
        return { id: 'request-' + next, state: 'custody' };
      }
      if (p === '/api/dm/agent/dismiss') { Object.assign(thread.agents[0], { state: 'dismissed', can_ask: false, can_dismiss: false, state_text: 'Dismissed fixture agent' }); return {}; }
      if (p === '/api/dm/agent/decide') { Object.assign(thread.agents[0], { state: body.accept ? 'active' : 'declined', can_decide: false }); return {}; }
      if (p === '/api/drive') return { status: 'unsupported' };
      if (p === '/api/typing/status') return { send: false, scopes: [] };
      return {};
    } };
  window.agentnet = provider;
  if (browser) window.agentnetEngine = provider;
  window.agentnetModules = { typing: { mountTyping: () => ({ setScope: async () => {}, stop: () => {} }) }, drivespace: { mount: () => ({}) } };
}
const seed = { me, peer, at, agentID };
const boot = '(' + bootstrap.toString().replace('SEED', JSON.stringify(seed)) + ')();';
const server = http.createServer((req, res) => {
  const name = new URL(req.url, 'http://127.0.0.1').pathname;
  if (name === '/fixture') { res.setHeader('Content-Type', 'text/html; charset=utf-8'); res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>External agent UI fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/app.css">' + markup + '<script src="/fixture.js"></script><script src="/assets/lenses.js"></script><script src="/assets/app.js"></script>'); }
  else if (name === '/fixture.js') { res.setHeader('Content-Type', 'text/javascript; charset=utf-8'); res.end(boot); }
  else if (name.startsWith('/assets/') && assets[name.slice(8)]) { res.setHeader('Content-Type', name.endsWith('.css') ? 'text/css; charset=utf-8' : 'text/javascript; charset=utf-8'); res.end(assets[name.slice(8)]); }
  else { res.statusCode = 404; res.end('fixture route unavailable'); }
});
let browser;
const checks = [], shots = [], errors = [], external = [], served = {};
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = 'http://127.0.0.1:' + server.address().port;
  browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  for (const mode of ['daemon', 'browser']) for (const width of [1280, 390]) {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 900 } });
    await context.route('**/*', r => { if (new URL(r.request().url()).origin === origin) return r.continue(); external.push(r.request().url()); return r.abort(); });
    const page = await context.newPage();
    page.on('pageerror', e => errors.push(e.message));
    page.on('response', async r => { const n = new URL(r.url()).pathname.slice(8); if (assetNames.includes(n)) served[n] = digest(await r.body()); });
    page.setDefaultTimeout(10000);
    const capture = async name => {
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, name + ' document overflow');
      assert(await page.evaluate(() => [...document.querySelectorAll('dialog[open]')].every(d => d.scrollWidth <= d.clientWidth + 1)), name + ' dialog overflow');
      const file = path.join(evidence, `external-ui-${mode}-${name}-${width}.png`); await page.screenshot({ path: file }); shots.push(file);
    };
    await page.goto(origin + '/fixture?mode=' + mode);
    await page.locator('#search').waitFor();
    if (width === 390) { await page.locator('#conv-list .contact-item').filter({ hasText: 'Alice Human' }).locator('button.contact').click(); }
    await page.locator('#agents').getByRole('button', { name: 'Invite an agent…', exact: true }).click();
    await page.locator('input[id="agent-host:outside/host"]').check();
    await page.locator('#invite-agent').selectOption(agentID);
    await page.locator('input[id="agent-share:chosen"]').check();
    assert.equal(await page.locator('input[id="agent-share:excluded"]').isChecked(), false);
    assert((await page.locator('#dialog').innerText()).includes('Unselected history and files are excluded'));
    await capture('invite-selected-context');
    await page.locator('#dialog-ok').click();
    await page.locator('#dialog').waitFor({ state: 'hidden' });
    assert.deepEqual(await page.evaluate(() => fixture.requests.find(r => r.path === '/api/dm/agent/invite').body.share), ['chosen']);
    await page.locator('#agents').getByRole('button', { name: 'Ask', exact: true }).click();
    const bytes = Buffer.from('current file \x00\xff exact bytes', 'binary');
    await page.locator('#file-input').setInputFiles({ name: 'current-request.bin', mimeType: 'application/octet-stream', buffer: bytes });
    await capture('file-only-ask');
    await page.locator('#send').click();
    await page.waitForFunction(() => fixture.requests.some(r => r.path === '/api/dm/agent/ask' && r.bytes));
    const ask = await page.evaluate(() => fixture.requests.find(r => r.path === '/api/dm/agent/ask'));
    assert.equal(ask.body.pid, 'outside-pid'); assert.equal(ask.body.kind, 'question'); assert.equal(ask.body.body, ''); assert.deepEqual(ask.bytes, [[...bytes]]);
    await page.locator('#body').fill('follow-up in same participation'); await page.locator('#send').click();
    await page.waitForFunction(() => fixture.requests.filter(r => r.path === '/api/dm/agent/ask').length === 2);
    await page.locator('#timeline').getByText('Fixture report for follow-up in same participation', { exact: true }).waitFor();
    await page.locator('#timeline').getByText('Fixture report for follow-up in same participation', { exact: true }).scrollIntoViewIfNeeded();
    await capture('reports-follow-up');
    await page.locator('#agents').getByRole('button', { name: 'Dismiss…', exact: true }).click(); await page.locator('#dialog-ok').click(); await page.locator('#dialog').waitFor({ state: 'hidden' });
    assert.equal(await page.locator('#send').isDisabled(), true);
    assert.equal(await page.evaluate(() => fixture.requests.some(r => r.path === '/api/dm/send')), false);
    // Synthetic visitor setup. Preserve production controls and provider grants.
    await page.evaluate(() => {
      fixture.thread.role = 'visitor'; fixture.thread.frozen = '';
      fixture.thread.messages = [{ id: 'snapshot', dir: 'in', from: 'claimed/author', claimed_key: 'claimed-key', excerpt_pid: 'outside-pid', synced_from: 'alice/laptop', body: 'Selected forwarded context', kind: 'task', actions: ['accept'], can: ['react', 'edit'], at: '2026-10-01T10:00:00Z' }];
      Object.assign(fixture.thread.agents[0], { state: 'invited', can_decide: window.agentnet.platform === 'daemon', can_ask: false, can_dismiss: false });
      fixture.change({ type: 'change', seq: ++fixture.overview.seq });
    });
    await page.locator('#notice').getByText('Only selected context and requests addressed to this agent are supplied. You cannot send ordinary room messages or change membership.', { exact: true }).waitFor();
    assert.equal(await page.locator('#composer').isHidden(), true);
    assert.equal(await page.locator('#agents').getByRole('button', { name: 'Invite an agent…', exact: true }).count(), 0);
    assert.equal(await page.locator('#timeline').getByRole('button', { name: 'Reply', exact: true }).count(), 0);
    assert.equal(await page.locator('#timeline [aria-label="Add a reaction"]').count(), 0);
    assert.equal(await page.locator('#timeline').getByRole('button', { name: 'Accept', exact: true }).count(), 0);
    await page.locator('#timeline details').getByText('Details', { exact: true }).click();
    assert((await page.locator('#timeline').innerText()).includes('authorship is not verified here'));
    await capture('visitor-claimed-context');
    checks.push({ mode, width, selectedOnly: true, exactFileBytes: true, followUpDismiss: true, visitorRestrictions: true });
    await context.close();
  }
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(served, hashes);
  fs.writeFileSync(path.join(evidence, 'fixture-result.json'), JSON.stringify({ pass: true, kind: 'inert provider rendering only', checks, shots, hashes, served, errors, external }, null, 2), { mode: 0o600 });
  console.log('PASS external agent UI rendered fixture: daemon/browser adapters, selected context, exact file-only asks, reports/follow-up/dismiss, visitor restrictions, 390/1280');
})().catch(async e => {
  console.error(e); console.error(JSON.stringify({ errors, external }));
  if (browser) for (const context of browser.contexts()) for (const page of context.pages()) {
    console.error((await page.locator('body').innerText()).slice(0, 2500));
    await page.screenshot({ path: path.join(evidence, 'failure.png') });
  }
  process.exitCode = 1;
}).finally(async () => { if (browser) await browser.close(); server.close(); });
