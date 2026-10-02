// Browser plugin not available: use already-installed Playwright/Chromium.
// Production page, CSS and renderer; isolated synthetic providers only.
// Run with AGENTNET_PLAYWRIGHT, AGENTNET_CHROMIUM and AGENTNET_SCREENSHOTS.
const fs = require('node:fs'), http = require('node:http'), path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const base = path.resolve(__dirname, '../static');
const fileBytes = Buffer.from('Synthetic release notes\nNo Hub, peer or model was contacted.\n');
const code = '```go\nfunc release(ctx context.Context) error {\n    // Keep interrupted work visible; never infer execution from emotion.\n    if err := verify(ctx); err != nil {\n        return fmt.Errorf("verification: %w", err)\n    }\n    return nil\n}\n```\n' +
  'Long technical reply: readable history and exact attribution must survive a narrow screen. '.repeat(7) + '\nEND OF FULL CODE REPLY';
const msg = (id, body, extra = {}) => ({ id, dir: 'in', from: 'studio/desk', to: 'mira/laptop', kind: 'message', body,
  at: '2026-09-30T12:00:00Z', author: { label: 'Builder (agent)', about: 'Synthetic agent author; label is separate from expression.' }, actions: [], ...extra });
const messages = [
  msg('root', 'Can we review the release plan before anything runs?', { dir: 'out', from: 'mira/laptop', to: 'studio/desk', emotion: 'neutral', author: { label: 'Mira (human)', about: 'Synthetic human author.' } }),
  msg('happy', 'Yes. Here is the plan for your review.', { kind: 'answer', reply_to: 'root', emotion: 'happy' }),
  msg('sad', 'One optional check failed. Nothing was launched.', { reply_to: 'root', emotion: 'sad', state: 'failed', state_text: 'Execution failed · review the recorded result' }),
  msg('focused', code, { reply_to: 'root', emotion: 'focused' }),
  msg('curious', 'Does the offline teammate need the attachment too?', { reply_to: 'happy', emotion: 'curious' }),
  msg('concerned', 'The teammate is offline. This file is a kept local copy.', { emotion: 'concerned', state: 'queued', state_text: 'Queued · peer offline; execution not confirmed', attachments: [{ name: 'release-notes.txt', size: fileBytes.length, index: 0, openable: true }] }),
  msg('celebrating', 'The draft is ready for a person to inspect.', { emotion: 'celebrating', state: 'needs_human', state_text: 'Needs human review · nothing runs automatically' }),
  msg('old-peer', 'Older peer sent no expression. Still readable.', { state: 'done', state_text: 'Recorded outcome: done' }),
  msg('unknown', 'Unrecognized expression falls back to neutral.', { emotion: 'searching', state: 'working', state_text: 'Working · workflow is separate from facial emotion' }),
  msg('deleted-parent', '', { deleted: true }),
  msg('deleted-reply', 'Reply to a deleted message keeps its reference.', { reply_to: 'deleted-parent', emotion: 'neutral' }),
  msg('missing-reply', 'Parent is not present in this captured history.', { reply_to: 'not-here', emotion: 'neutral' }),
  msg('review', 'Please run the release verification after you approve this request.', { kind: 'task', emotion: 'concerned', state: 'pending', state_text: 'Needs you: accept this task before it can run', actions: ['accept', 'decline'], detail: 'Prototype only: this fixture records the decision and executes nothing.' }),
];
const thread = { id: 'comic-root', peer: 'studio/desk', key: { pinned: 'SHA256:fixture' }, messages };
const overview = { demo: false, me: { address: 'mira/laptop', fingerprint: 'SHA256:fixture' }, device: { online: false, revoked: false },
  person: { person: 'p-mira', label: 'Mira', address: 'mira/laptop', state: 'self' }, people: [], agents: false,
  threads: [{ id: thread.id, peer: thread.peer, title: messages[0].body, last_at: messages[0].at, count: messages.length, unread: 0, review: 1 }],
  dms: [], review: [], quarantine: [], seq: 0,
  directory: { status: 'listed', current: true, at: messages[0].at, truncated: false, members: [{ address: 'studio/desk', presence: 'offline' }] } };
const bootstrap = `window.agentnet={platform:'daemon',skins:[],onOpen:()=>{},workspace:{id:'prototype'},
 api:async(path,body)=>{const r=await fetch('/fixture-api',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({path,body})});if(!r.ok)throw Error(await r.text());return r.json()},
 file:async(id,i,dir)=>{const r=await fetch('/api/files/'+encodeURIComponent(id)+'/'+i+'?dir='+encodeURIComponent(dir));if(!r.ok)throw Error(await r.text());return {bytes:new Uint8Array(await r.arrayBuffer())}},listen:()=>()=>{}};
 window.agentnetModules={typing:{mountTyping:()=>({setScope:async()=>{},stop(){},disconnect(){},destroy(){},showSettings(){}})}};`;
const requests = [], files = [], errors = [], external = [], checks = [], shots = [];
const server = http.createServer(async (req, res) => {
  const u = new URL(req.url, 'http://fixture');
  if (u.pathname === '/fixture-api') {
    let raw = ''; for await (const chunk of req) raw += chunk;
    const request = JSON.parse(raw), route = new URL(request.path, 'http://fixture').pathname;
    requests.push(request);
    let data;
    if (route === '/api/overview') data = overview;
    else if (route === '/api/thread') data = thread;
    else if (route === '/api/act') data = { note: 'Synthetic decision recorded; nothing executed.' };
    else if (route === '/api/refresh') data = { text: 'Synthetic offline workspace' };
    else if (route === '/api/notify/seen') data = {};
    else { res.writeHead(404).end('Unexpected synthetic API route: ' + route); return; }
    res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data)); return;
  }
  if (u.pathname === '/api/files/concerned/0' && u.searchParams.get('dir') === 'in') {
    files.push(u.pathname + u.search); res.setHeader('Content-Type', 'application/octet-stream'); res.end(fileBytes); return;
  }
  let content, type = 'text/javascript';
  if (u.pathname === '/') {
    type = 'text/html'; content = '<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>AgentNet Comic prototype</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/assets/app.css">' +
      fs.readFileSync(path.join(base, 'default.html'), 'utf8') + '<script src="/fixture.js"></script><script src="/assets/lenses.js"></script><script src="/assets/app.js"></script>';
  } else if (u.pathname === '/fixture.js') content = bootstrap;
  else if (['/assets/app.css', '/assets/app.js', '/assets/lenses.js', '/assets/icon-192.png'].includes(u.pathname)) {
    type = u.pathname.endsWith('.css') ? 'text/css' : u.pathname.endsWith('.png') ? 'image/png' : type;
    content = fs.readFileSync(path.join(base, u.pathname.endsWith('.png') ? 'ant.png' : path.basename(u.pathname)));
  } else { res.writeHead(404).end(); return; }
  res.setHeader('Content-Type', type + (type === 'image/png' ? '' : '; charset=utf-8'));
  res.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: blob:; font-src 'self'");
  res.end(content);
});
(async () => {
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  let browser;
  try {
    browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
    const origin = 'http://127.0.0.1:' + server.address().port;
    for (const width of [1280, 390]) {
      const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 900 }, reducedMotion: 'reduce', acceptDownloads: true });
      await context.route('**/*', r => {
        if (r.request().url().startsWith(origin + '/') || r.request().url().startsWith('data:')) return r.continue();
        external.push(r.request().url()); return r.abort();
      });
      const page = await context.newPage();
      page.on('pageerror', e => errors.push(e.message));
      page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
      page.setDefaultTimeout(10000);
      await page.goto(origin);
      await page.waitForFunction(() => state.overview);
      await page.evaluate(() => openThread('comic-root'));
      await page.waitForFunction(() => state.data?.id === 'comic-root');
      assert.equal(await page.title(), 'AgentNet Comic prototype');
      assert.equal(await page.evaluate(() => state.lens), 'classic');
      assert.match(await page.locator('#offline').innerText(), /Not connected to your server/);
      const capture = async name => {
        const layout = await page.evaluate(() => ({ overflow: document.documentElement.scrollWidth > innerWidth,
          regions: [...document.querySelectorAll('#comic .page.current, dialog[open]')].map(n => ({ scroll: n.scrollWidth, client: n.clientWidth })),
          faces: [...document.querySelectorAll('#comic .page.current .comic-expression')].map(n => {
            const face = n.closest('.comic-face').getBoundingClientRect(), control = n.closest('.panel').querySelector('.read-btn').getBoundingClientRect();
            return { animation: getComputedStyle(n).animationName, font: getComputedStyle(n).fontSize,
              covered: face.left < control.right && control.left < face.right && face.top < control.bottom && control.top < face.bottom };
          }) }));
        assert.equal(layout.overflow, false, name + ': document overflow');
        assert(layout.regions.every(n => n.scroll <= n.client + 1), name + ': region overflow');
        assert(layout.faces.every(n => n.animation === 'none' && parseFloat(n.font) >= 27 && !n.covered), name + ': static readable faces without control overlap');
        if (process.env.AGENTNET_SCREENSHOTS) {
          const file = path.join(process.env.AGENTNET_SCREENSHOTS, `comic-${name}-${width}.png`);
          await page.screenshot({ path: file }); shots.push(file);
        }
      };
      await capture('classic');
      for (let up = 0; up < 3 && !(await page.locator('#profile-btn').isVisible()); up++) await page.locator('#back').click();
      await page.locator('#profile-btn').click();
      await page.getByRole('tab', { name: 'Appearance', exact: true }).click();
      await page.locator('#lens [data-lens="comic"]').click();
      await page.locator('#settings-close').click();
      if (!(await page.locator('#comic').isVisible())) await page.evaluate(() => openThread('comic-root'));
      await page.evaluate(() => Comic.turn(0, 0));
      await capture('cover');
      await page.getByRole('button', { name: 'Start reading', exact: true }).click();
      assert.equal(await page.locator('#comic .page-count').innerText(), 'Page 1 of ' + await page.evaluate(() => Comic.list.length - 1));
      const count = await page.evaluate(() => Comic.list.length);
      const seen = new Map();
      for (let i = 1; i < count; i++) {
        await page.evaluate(i => Comic.turn(0, i), i);
        const faces = await page.locator('#comic .page.current .comic-face').evaluateAll(ns => ns.map(n => ({ id: n.closest('.panel').id.slice(2), emotion: n.dataset.emotion, label: n.getAttribute('aria-label') })));
        faces.forEach(f => seen.set(f.id, f));
        await capture('page-' + i);
        for (const panel of await page.locator('#comic .page.current .panel').all()) {
          const clipped = await panel.evaluate(n => {
            const r = n.getBoundingClientRect(), p = n.closest('.page').getBoundingClientRect();
            return r.top < p.top || r.bottom > p.bottom;
          });
          if (clipped) {
            const id = await panel.getAttribute('id');
            await panel.evaluate(n => n.scrollIntoView({ block: 'start' }));
            await capture('page-' + i + '-' + id);
          }
        }
      }
      for (const emotion of ['neutral', 'happy', 'sad', 'focused', 'curious', 'concerned', 'celebrating']) {
        const id = emotion === 'neutral' ? 'root' : emotion;
        assert.equal(seen.get(id).emotion, emotion); assert.match(seen.get(id).label, /prototype/);
      }
      assert.equal(seen.get('old-peer').emotion, 'neutral'); assert.equal(seen.get('unknown').emotion, 'neutral');
      await page.evaluate(() => Comic.focus('sad'));
      assert.match(await page.locator('#p-sad').innerText(), /Execution failed/i);
      await page.locator('#p-sad .comic-reply').click();
      assert.equal(await page.evaluate(() => document.activeElement.closest('.panel')?.id), 'p-root');
      assert.match(await page.locator('#p-root .panel-foot').innerText(), /Mira \(human\)/);
      await page.evaluate(() => Comic.focus('happy'));
      assert.match(await page.locator('#p-happy .panel-foot').innerText(), /Builder \(agent\)/);
      await page.evaluate(() => Comic.focus('deleted-reply'));
      assert.equal(await page.locator('#p-deleted-reply .comic-reply').innerText(), 'Reply to: (deleted message)');
      await page.evaluate(() => Comic.focus('missing-reply'));
      assert.equal(await page.locator('#p-missing-reply .comic-reply').innerText(), 'Reply to a message not shown here');
      assert.equal(await page.locator('#p-missing-reply .comic-reply').evaluate(n => n.tagName), 'SPAN');
      await page.evaluate(() => Comic.focus('focused'));
      await page.locator('#p-focused .read-btn').click();
      assert.equal(await page.locator('#dialog .quote').innerText(), code);
      assert.equal(await page.locator('#dialog .quote').evaluate(n => {
        const range = document.createRange(); range.selectNodeContents(n);
        const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
        const selected = selection.toString(); selection.removeAllRanges(); return selected;
      }), code);
      await capture('full-code');
      await page.keyboard.press('Escape'); await page.locator('#dialog').waitFor({ state: 'hidden' });
      await page.evaluate(() => Comic.focus('concerned'));
      await page.locator('#p-concerned .read-btn').click();
      await page.locator('#dialog').getByRole('button', { name: 'Open', exact: true }).click();
      const downloadReady = page.waitForEvent('download');
      await page.locator('#dialog').getByRole('link', { name: 'Download release-notes.txt', exact: true }).click();
      const download = await downloadReady;
      assert.equal(download.suggestedFilename(), 'release-notes.txt');
      assert.deepEqual(fs.readFileSync(await download.path()), fileBytes);
      await capture('attachment-open');
      await page.keyboard.press('Escape');
      await page.evaluate(() => Comic.focus('review'));
      const actsBefore = requests.filter(r => r.path === '/api/act').length;
      await page.locator('#p-review .acts button').first().click();
      assert.equal(await page.locator('#dialog-title').innerText(), 'Run this task on your computer?');
      assert.equal(await page.locator('#dialog-ok').isDisabled(), true);
      assert.equal(await page.evaluate(() => document.activeElement.id), 'dialog-cancel');
      await capture('review-unchecked');
      await page.keyboard.press('Escape');
      assert.equal(requests.filter(r => r.path === '/api/act').length, actsBefore);
      await page.locator('#p-review .acts button').first().click();
      await page.locator('#gate').check(); assert.equal(await page.locator('#dialog-ok').isEnabled(), true);
      await page.locator('#dialog-ok').click(); await page.locator('#dialog').waitFor({ state: 'hidden' });
      assert.deepEqual(requests.filter(r => r.path === '/api/act').at(-1).body, { do: 'accept', id: 'review' });
      await page.evaluate(() => { Comic.turn(0, 1); document.activeElement.blur(); });
      await page.keyboard.press('ArrowRight'); assert.equal(await page.evaluate(() => Comic.pageOf[Comic.t.id]), 2);
      await page.keyboard.press('ArrowLeft'); assert.equal(await page.evaluate(() => Comic.pageOf[Comic.t.id]), 1);
      assert.equal(await page.locator('#comic .leaf').count(), 0);
      await capture('reduced-motion');
      await page.getByRole('button', { name: 'All pages', exact: true }).click(); await capture('all-pages');
      await page.getByRole('button', { name: 'Page 1', exact: true }).click();
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      await page.getByRole('button', { name: 'Next page', exact: true }).click();
      await page.waitForFunction(() => !document.querySelector('#comic .leaf'));
      await capture('static-after-turn');
      checks.push({ width, sevenExpressions: true, neutralFallback: true, statusSeparate: true, localReplyBranch: true,
        codeInFull: true, attachmentBytes: true, offlineVisible: true, gatedSyntheticReview: true, keyboardAndGrid: true, reducedAndStatic: true });
      await context.close();
    }
    assert.deepEqual(errors, []); assert.deepEqual(external, []);
    assert.equal(files.length, 2);
    console.log(JSON.stringify({ pass: true, checks, shots, errors, externalRequests: external, syntheticFileRequests: files }));
  } catch (e) {
    const page = browser?.contexts().at(-1)?.pages().at(-1);
    if (page) {
      console.error(JSON.stringify({ errors, requests: requests.map(r => r.path), text: await page.locator('body').innerText() }));
      if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, 'comic-failure.png') });
    }
    throw e;
  } finally { if (browser) await browser.close(); await new Promise(r => server.close(r)); }
})().catch(e => { console.error(e); process.exitCode = 1; });
