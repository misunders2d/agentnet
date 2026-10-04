// The production host (loader.js) on a demo daemon with the real catalog:
//   - Comic mounts as a package in a shadow root: its fonts and Tailwind's
//     registered properties are adopted at document level, popups render in
//     its own root, and it shows no host switcher;
//   - saved "default"/"classic" open Comic and are rewritten; ?skin=default
//     is Comic;
//   - an installed skin asks for trust first (with the switcher already
//     there), then mounts in its own shadow root under the switcher, which
//     never covers it, works by keyboard and follows light and dark;
//   - notifications a skin does not take are offered in the switcher, kept
//     until opened in Comic; an unknown workspace opens nothing;
//   - a browser-local skin is imported from the switcher, asks for trust,
//     asks again when its bytes change, and is removed again;
//   - one step back to Comic, and reload keeps the choice.
//   node skins_browser_check.cjs DAEMON_URL_WITH_TOKEN NOTEBOOK_DIR
const fs = require('fs'), path = require('path'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target, notebook] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
const local = (version) => {
  const manifest = { api: 1, id: 'pad', name: 'Scratch pad', entry: 'entry.mjs', style: 'style.css', files: ['entry.mjs', 'style.css'] };
  const files = { 'skin.json': JSON.stringify(manifest), 'entry.mjs': `export function mount(root,host){const h=document.createElement('h1');h.textContent='Scratch pad ${version}';root.append(h);host.onOpen(()=>{});}export function unmount(root){root.replaceChildren();}`, 'style.css': 'h1 { color: rgb(12, 80, 60); margin: 24px; }' };
  return Object.entries(files).map(([name, content]) => ({ name, mimeType: 'application/octet-stream', buffer: Buffer.from(content) }));
};

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    for (const [tag, width, height, scheme] of [['desk-light', 1800, 960, 'light'], ['phone-dark', 390, 844, 'dark']]) {
      const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme });
      const page = await ctx.newPage(), errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      page.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errors.push(m.text()); });
      page.on('response', (r) => { const p = new URL(r.url()).pathname; if (r.status() >= 400 && !['/api/workspaces', '/api/agents', '/api/dm', '/api/refresh', '/favicon.ico'].includes(p)) errors.push(r.status() + ' ' + p); }); // the demo has no workspaces, agent catalog or DMs (the synthetic notification names one)
      const snap = (n) => shots && page.screenshot({ path: path.join(shots, 'skins-' + tag + '-' + n + '.png') });
      const origin = new URL(target).origin;
      // Comic is up: its root is mounted and has drawn (the chat list, or the conversation a destination opened).
      const comicUp = () => page.waitForFunction(() => { const sr = document.getElementById('skin').shadowRoot, r = sr && sr.querySelector('.an-root'); return !!r && r.innerText.length > 20; }, null, { timeout: 20000 });
      const noScroll = async (what) => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': horizontal scroll ' + what);

      // Comic, from a saved "default".
      await page.goto(target);
      await page.evaluate(() => localStorage.setItem('agentnet.skin', 'default'));
      await page.goto(origin + '/');
      await comicUp();
      const comic = await page.evaluate(async () => {
        await document.fonts.ready;
        const sr = document.getElementById('skin').shadowRoot, root = sr && sr.querySelector('.skin-root');
        const kept = document.adoptedStyleSheets.flatMap((s) => [...s.cssRules].map((r) => r.constructor.name));
        return { shadow: !!sr, theme: root && root.dataset.theme, kids: [...document.body.children].map((e) => e.id), saved: localStorage.getItem('agentnet.skin'),
          onest: [...document.fonts].some((f) => f.family === 'Onest' && f.status === 'loaded'), kinds: [...new Set(kept)].sort(),
          shadowPop: getComputedStyle(sr.querySelector('button')).boxShadow };
      });
      assert(comic.shadow, tag + ': Comic in a shadow root');
      assert.equal(comic.theme, scheme, tag + ': Comic follows the device theme');
      assert.deepEqual(comic.kids, ['skin'], tag + ': Comic shows no switcher');
      assert.equal(comic.saved, 'comic', tag + ': saved default rewritten');
      assert(comic.onest, tag + ': Comic fonts loaded from its package');
      assert.deepEqual(comic.kinds, ['CSSFontFaceRule', 'CSSPropertyRule'], tag + ': document rules are faces and properties only');
      await noScroll('Comic');
      await snap('comic');
      for (const saved of ['classic', '']) {
        await page.evaluate((s) => s ? localStorage.setItem('agentnet.skin', s) : localStorage.removeItem('agentnet.skin'), saved);
        await page.goto(origin + '/');
        await comicUp();
        assert.equal(await page.evaluate(() => localStorage.getItem('agentnet.skin')), 'comic', tag + ': saved "' + saved + '" opens Comic');
      }
      await page.goto(origin + '/?skin=default');
      await comicUp();

      // The installed Notebook: trust first, the switcher already there.
      await page.goto(origin + '/?skin=notebook');
      const trust = page.getByRole('button', { name: 'Use this skin' });
      await trust.waitFor();
      assert.equal(await page.locator('#skin-bar').count(), 1, tag + ': switcher before trust');
      assert.equal(await page.evaluate(() => !!document.getElementById('skin').shadowRoot), false, tag + ': nothing mounted before trust');
      await noScroll('trust');
      await snap('trust');
      await trust.click();
      await page.getByRole('heading', { name: 'Notebook', exact: true }).waitFor();
      const layout = await page.evaluate(() => {
        const bar = document.getElementById('skin-bar').getBoundingClientRect(), skin = document.getElementById('skin').getBoundingClientRect();
        return { barBottom: bar.bottom, skinTop: skin.top, skinBottom: skin.bottom, h: innerHeight, scheme: document.getElementById('skin-bar').dataset.scheme };
      });
      assert(layout.barBottom <= layout.skinTop + 0.5, tag + ': the switcher never covers the skin');
      assert(Math.abs(layout.skinBottom - layout.h) < 1, tag + ': the skin fills the rest of the page');
      assert.equal(layout.scheme, scheme, tag + ': switcher follows the theme');
      await noScroll('Notebook');
      await snap('notebook');
      // Keyboard: the skin menu opens with the arrow key, closes with Escape.
      await page.getByRole('button', { name: /^Skin: / }).focus();
      await page.keyboard.press('ArrowDown');
      await page.getByRole('menu', { name: 'Skins' }).waitFor();
      assert.equal(await page.evaluate(() => document.getElementById('skin-bar').shadowRoot.activeElement.getAttribute('aria-checked')), 'true', tag + ': focus on the skin in use');
      await page.keyboard.press('ArrowDown');
      await page.waitForTimeout(250);
      await snap('skin-menu');
      await page.keyboard.press('Escape');
      await page.getByRole('menu', { name: 'Skins' }).waitFor({ state: 'hidden' });

      // Notifications Notebook does not take wait in the switcher.
      const mid = 'a'.repeat(32), conv = 'b'.repeat(64);
      await page.evaluate((h) => { location.hash = h; }, '#review');
      await page.getByText('Something is waiting for your decision.').waitFor();
      await snap('notice');
      await page.evaluate((h) => { location.hash = h; }, '#msg=' + mid + '&conv=' + conv + '&dir=in&workspace=unknown');
      await page.getByText('A notification is for a workspace that isn’t on this device.').waitFor();
      await page.evaluate((h) => { location.hash = h; }, '#msg=' + mid + '&conv=' + conv + '&dir=in');
      await page.getByText('A notification is waiting for you.').waitFor();
      await page.getByRole('button', { name: 'Open it in Comic' }).click();
      await comicUp();
      assert.equal(new URL(page.url()).hash, '', tag + ': Comic took the message destination');
      assert.equal(new URL(page.url()).searchParams.get('skin'), 'comic');

      // A browser-local skin, from the switcher.
      await page.goto(origin + '/?skin=notebook');
      await page.getByRole('heading', { name: 'Notebook', exact: true }).waitFor();
      await page.getByRole('button', { name: /^Skin: / }).click();
      await page.getByRole('menuitem', { name: /Import or remove skins/ }).click();
      await page.getByLabel('Import skin files', { exact: true }).setInputFiles(local(1));
      await page.getByText(/Stored Scratch pad/).waitFor();
      await snap('import');
      await page.getByRole('button', { name: 'Done' }).click();
      await page.getByRole('button', { name: /^Skin: / }).click();
      await page.getByRole('menuitemradio', { name: /Scratch pad/ }).click();
      await page.getByRole('button', { name: 'Use this skin' }).click();
      await page.getByRole('heading', { name: 'Scratch pad 1' }).waitFor();
      assert.equal(await page.getByRole('heading', { name: 'Scratch pad 1' }).evaluate((e) => getComputedStyle(e).color), 'rgb(12, 80, 60)', tag + ': local CSS in its shadow root');
      await page.reload();
      await page.getByRole('heading', { name: 'Scratch pad 1' }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Use this skin' }).count(), 0, tag + ': same digest keeps trust');
      await page.getByRole('button', { name: /^Skin: / }).click();
      await page.getByRole('menuitem', { name: /Import or remove skins/ }).click();
      await page.getByLabel('Import skin files', { exact: true }).setInputFiles(local(2));
      await page.getByText(/Stored Scratch pad/).waitFor();
      await page.getByRole('button', { name: 'Done' }).click();
      await page.reload();
      await page.getByRole('button', { name: 'Use this skin' }).waitFor(); // changed bytes: asked again
      await page.getByRole('button', { name: 'Use this skin' }).click();
      await page.getByRole('heading', { name: 'Scratch pad 2' }).waitFor();
      await page.getByRole('button', { name: /^Skin: / }).click();
      await page.getByRole('menuitem', { name: /Import or remove skins/ }).click();
      page.once('dialog', (d) => d.accept());
      await page.getByRole('button', { name: 'Remove Scratch pad' }).click();
      await page.getByText('Removed from this browser.').waitFor();
      await page.getByRole('button', { name: 'Done' }).click();
      await page.reload();
      await comicUp(); // the removed skin is gone: Comic opens
      assert.equal(await page.evaluate(() => localStorage.getItem('agentnet.skin')), 'comic');

      // One step back to Comic; reload keeps it.
      await page.goto(origin + '/?skin=notebook');
      await page.getByRole('button', { name: 'Switch to Comic' }).click();
      await comicUp();
      await page.reload();
      await comicUp();
      assert.deepEqual(errors, [], tag + ': page errors');
      await ctx.close();
      console.log(tag + ': ok');
    }
    console.log('skins browser check PASS: Comic package (shadow root, fonts, properties), saved choices, installed and browser-local skins with trust, switcher layout/keyboard/theme, notifications, back to Comic; 1800x960 light, 390x844 dark');
  } finally { await browser.close(); }
})().catch((e) => { console.error(e); process.exitCode = 1; });
