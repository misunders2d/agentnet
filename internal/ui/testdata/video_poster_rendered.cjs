// MEL-750: a received video's card shows a still made here from the file,
// with Play over it, in Chromium, Firefox and WebKit at 1440 and 390. The
// capture never plays, the composer stays usable while it waits, and cards
// without a still keep Play (when the file can play here) and Download.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const pw = require(process.env.AGENTNET_PLAYWRIGHT);

const shots = process.env.AGENTNET_SCREENSHOTS;
const url = process.argv[2];

async function check(kind, phone) {
  const launch = kind === 'chromium' ? { executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] } : {};
  const browser = await pw[kind].launch({ headless: true, ...launch });
  const label = kind + '-' + (phone ? 390 : 1440);
  const errors = [], consoleErrors = [];
  try {
    const ctx = await browser.newContext({ viewport: phone ? { width: 390, height: 844 } : { width: 1440, height: 900 }, hasTouch: phone });
    await ctx.addInitScript(() => {
      window.videoPlays = 0;
      const play = HTMLMediaElement.prototype.play;
      HTMLMediaElement.prototype.play = function () { if (this.tagName === 'VIDEO') window.videoPlays++; return play.call(this); };
    });
    const p = await ctx.newPage();
    p.setDefaultTimeout(10000);
    p.on('pageerror', e => errors.push(String(e)));
    p.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text()); });
    // Hold the received video's first read: its poster waits, nothing else may.
    let release; const held = new Promise(r => { release = r; });
    let heldOnce = false;
    await p.route('**/api/files/picture-message/8?dir=in', async route => {
      if (!heldOnce) { heldOnce = true; await held; }
      await route.continue();
    });
    await p.goto(url);
    await p.getByRole('button', { name: /^Bob/ }).first().click();
    const card = name => p.getByRole('group', { name: 'Video ' + name, exact: true });
    const frame = name => card(name).locator('[data-poster]');
    const playButton = name => card(name).getByRole('button', { name: 'Play video ' + name, exact: true });
    await frame('received.webm').scrollIntoViewIfNeeded();
    await p.waitForFunction(el => el.dataset.poster === 'loading', await frame('received.webm').elementHandle());
    assert.equal(await playButton('received.webm').count(), 1, 'Play is there while the poster is made');
    // The composer answers while the capture waits on its read.
    const box = p.getByRole('textbox', { name: /Message Bob/ });
    await box.click();
    const typed = Date.now();
    await box.press('x');
    await p.waitForFunction(el => (el.value ?? el.textContent).includes('x'), await box.elementHandle());
    const typingMs = Date.now() - typed;
    assert(typingMs < 1100, label + ' typing while a poster waits: ' + typingMs + ' ms');
    release();
    const asked = Date.now();
    await p.waitForFunction(el => el.dataset.poster !== 'loading', await frame('received.webm').elementHandle(), { timeout: 8000 });
    const posterMs = Date.now() - asked;
    const state = await frame('received.webm').getAttribute('data-poster');
    assert.equal(state, 'ready', label + ' poster from the local file');
    assert.equal(await frame('received.webm').locator('img').evaluate(im => im.complete && im.naturalWidth > 0), true, 'still decoded');
    const geo = await frame('received.webm').boundingBox();
    assert(Math.abs(geo.width / geo.height - 160 / 120) < .03, 'video ratio kept: ' + JSON.stringify(geo));
    assert(geo.height <= (phone ? 844 : 900) * .6 + 1, 'bounded like pictures');
    assert.equal(await card('received.webm').locator('video').count(), 0, 'no player before Play');
    assert.equal(await p.evaluate(() => window.videoPlays), 0, 'poster never plays');
    // Sent: the poster comes from this device's kept copy.
    await frame('sent.webm').scrollIntoViewIfNeeded();
    await p.waitForFunction(el => el.dataset.poster === 'ready', await frame('sent.webm').elementHandle(), { timeout: 8000 });
    assert.equal(await playButton('sent.webm').count(), 1, 'sent video plays from its poster');
    // Files without a still: Play stays where it could play; big files only Download.
    for (const name of ['spoof.mp4', 'broken.webm']) {
      await frame(name).scrollIntoViewIfNeeded();
      await p.waitForFunction(el => el.dataset.poster === 'none', await frame(name).elementHandle(), { timeout: 8000 });
      assert.equal(await playButton(name).count(), 1, name + ' keeps Play');
      assert.equal(await card(name).getByRole('button', { name: 'Download ' + name, exact: true }).count(), 1, name + ' keeps Download');
    }
    await frame('large.webm').scrollIntoViewIfNeeded();
    assert.equal(await frame('large.webm').getAttribute('data-poster'), 'waiting');
    assert.equal(await playButton('large.webm').count(), 0);
    await card('large.webm').getByText('Videos up to 16 MiB can play here.', { exact: false }).waitFor();
    assert.equal(await card('large.webm').getByRole('button', { name: 'Download large.webm', exact: true }).count(), 1);
    // Playback opens from the poster: the existing player.
    await frame('received.webm').scrollIntoViewIfNeeded();
    if (shots) await p.screenshot({ path: path.join(shots, `video-poster-${label}.png`) });
    const clicked = Date.now();
    await playButton('received.webm').click();
    const video = card('received.webm').locator('video');
    await video.waitFor();
    const openMs = Date.now() - clicked;
    await p.waitForFunction(v => v.readyState >= 2 && v.videoWidth === 160, await video.elementHandle());
    assert.equal(await video.evaluate(v => v.autoplay), false, 'no autoplay attribute');
    await card('received.webm').getByRole('button', { name: 'Close video received.webm', exact: true }).click();
    await video.waitFor({ state: 'detached' });
    assert.equal(await frame('received.webm').getAttribute('data-poster'), 'ready', 'Close returns to the same poster');
    assert.deepEqual(errors, [], label + ' page errors');
    return { browser: label, posterMs, typingMs, openMs, consoleErrors: consoleErrors.length };
  } finally {
    await browser.close();
  }
}

(async () => {
  // Linux hosts without Playwright WebKit's libraries run WebKit in CI (macOS).
  const engines = (process.env.AGENTNET_VIDEO_ENGINES || 'chromium firefox webkit').split(/\s+/).filter(Boolean);
  const results = [];
  for (const kind of engines)
    for (const phone of [false, true]) results.push(await check(kind, phone));
  console.log(JSON.stringify(results));
  console.log('video poster rendered PASS: ' + engines.join(', ') + ' at 1440 and 390');
})().catch(e => { console.error(e); process.exit(1); });
