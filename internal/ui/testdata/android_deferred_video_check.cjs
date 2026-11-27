// Real Comic and attachment reads. The Android bridge and relay replies are
// controlled so Play can be checked with a held, offline, and successful fetch.
const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  try {
    const page = await browser.newPage({ viewport: { width: 390, height: 844 }, hasTouch: true });
    page.setDefaultTimeout(6000);
    await page.addInitScript(() => {
      window.__agentnetPlatform = 'android';
      window.__agentnetOnBack = fn => { window.videoBack = fn; return () => { delete window.videoBack; }; };
      window.__agentnetAndroid = { visible: () => true, onVisibility: () => () => {},
        legacyDrafts: async () => ({ drafts: [] }), acknowledgeLegacyDraft: async () => ({ removed: true }) };
      Object.defineProperty(Document.prototype, 'fullscreenEnabled', { configurable: true, get: () => false });
    });
    const errors = [];
    page.on('pageerror', e => errors.push(String(e)));
    const card = name => page.getByRole('group', { name: 'Video ' + name, exact: true });
    const received = card('received.mp4');
    const play = received.getByRole('button', { name: 'Play video received.mp4', exact: true });
    const close = received.getByRole('button', { name: 'Close video received.mp4', exact: true });
    const progress = received.getByRole('progressbar', { name: 'Downloading received.mp4' });
    const download = received.getByRole('button', { name: /^Download received\.mp4/ });
    const composer = page.locator('[data-part="composer"] textarea');
    const scroll = () => page.getByRole('log', { name: 'Messages' }).evaluate(el => el.parentElement.scrollTop);
    const size = 12 * 1024 * 1024;
    let failFetch, finishFetch, finishProgress;
    const offline = new Promise(resolve => { failFetch = resolve; });
    const fetch = new Promise(resolve => { finishFetch = resolve; });
    const complete = new Promise(resolve => { finishProgress = resolve; });
    let attempts = 0, reads = 0;
    const ref = '**/api/files/video-16/16';
    await page.route(ref + '?dir=in', route => { reads++; return route.continue(); });
    await page.route(ref + '/fetch?dir=in', async route => {
      assert.equal(route.request().method(), 'POST');
      if (++attempts === 1) { await offline; await route.abort('internetdisconnected'); return; }
      await fetch;
      await route.fulfill({ json: { have: size / 2, total: size, fetching: true } });
    });
    await page.route(ref + '/progress?dir=in&after=' + size / 2, async route => {
      await complete;
      await route.fulfill({ json: { have: size, total: size, done: true } });
    });
    await page.goto(process.argv[2]);
    await page.getByRole('button', { name: /^Bob/ }).first().click();
    await download.waitFor();
    await play.scrollIntoViewIfNeeded();
    assert((await play.boundingBox()).height >= 44, 'waiting MP4 has a visible Play target');
    assert.equal(reads, 0, 'waiting video never reads automatically');

    const started = Date.now();
    await play.tap();
    await progress.waitFor();
    const feedbackMs = Date.now() - started;
    assert(feedbackMs < 1100, 'progress appears on tap during a held fetch: ' + feedbackMs + ' ms');
    assert.equal(await play.getAttribute('aria-busy'), 'true');
    await composer.fill('Draft typed while the MP4 is downloading');
    failFetch();
    await received.getByText(/Couldn’t download: AgentNet is not reachable/).waitFor();
    await received.getByRole('button', { name: 'Retry download received.mp4' }).waitFor();
    assert.equal(await received.locator('video').count(), 0);
    assert.equal(reads, 0);

    await play.tap();
    await close.tap();
    await close.waitFor({ state: 'hidden' });
    await progress.waitFor();
    await play.tap(); // Rejoin the active fetch, without another POST.
    await close.waitFor();
    finishFetch();
    await progress.and(page.locator('[aria-valuenow="50"]')).waitFor();
    assert.equal(reads, 0, 'the file is read only after it is here');
    finishProgress();
    const video = received.locator('video');
    await page.waitForFunction(v => v.readyState >= 2 && v.videoWidth === 160, await video.elementHandle());
    assert.deepEqual(await video.evaluate(v => [v.paused, v.currentTime, v.controls, v.playsInline, v.autoplay]), [true, 0, true, true, false]);
    assert.equal(attempts, 2);
    assert.equal(reads, 1, 'Play reads the file once, without a concurrent poster read');
    await download.waitFor();
    await video.evaluate(v => v.play());
    await page.waitForFunction(v => !v.paused && v.currentTime > .1, await video.elementHandle());
    await video.evaluate(v => { v.pause(); v.currentTime = 1.5; });
    await page.waitForFunction(v => !v.seeking && Math.abs(v.currentTime - 1.5) < .15, await video.elementHandle());
    const expand = received.getByRole('button', { name: 'Expand video received.mp4' });
    await expand.scrollIntoViewIfNeeded();
    const before = await scroll();
    await video.evaluate(inline => {
      const root = inline.getRootNode();
      const deferPosition = event => {
        if (event.target === inline || !event.target.closest('[role="dialog"]')) return;
        root.removeEventListener('loadedmetadata', deferPosition, true);
        // Close after metadata is readable but before the viewer handles it.
        event.stopImmediatePropagation();
      };
      root.addEventListener('loadedmetadata', deferPosition, true);
    });
    await expand.tap();
    const viewer = page.getByRole('dialog', { name: 'received.mp4', exact: true });
    await viewer.waitFor();
    await page.waitForFunction(v => v.readyState >= 1 && v.currentTime === 0, await viewer.locator('video').elementHandle());
    assert.equal(await page.evaluate(() => window.videoBack()), true);
    await viewer.waitFor({ state: 'hidden' });
    assert.deepEqual(await video.evaluate(v => [v.paused, Math.abs(v.currentTime - 1.5) < .15]), [true, true]);
    assert.equal(await composer.inputValue(), 'Draft typed while the MP4 is downloading');
    assert.equal(await scroll(), before, 'Back leaves the conversation scroll in place');

    const sent = card('sent.mp4');
    await sent.getByRole('button', { name: 'Play video sent.mp4', exact: true }).tap();
    const sentVideo = sent.locator('video');
    await page.waitForFunction(v => v.readyState >= 2 && v.videoWidth === 160, await sentVideo.elementHandle());
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ feedbackMs, attempts, receivedMP4: 'paused start, played, paused, sought', sentMP4: 'decoded', back: 'draft and scroll retained' }));
  } finally {
    await browser.close();
  }
})().catch(e => { console.error(e); process.exit(1); });
