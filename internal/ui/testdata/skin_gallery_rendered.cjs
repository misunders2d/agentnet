// The skin gallery end to end on the production host (loader.js) and Comic,
// with GitHub replaced by a local fake (no network):
//   - Settings → Appearance lists the latest published release's skins with
//     verified previews; a skin that needs a newer AgentNet cannot be
//     installed; nothing but the two fixed gallery sources is contacted;
//   - Install stores the package in this browser, Comic offers "Use", the
//     host asks for trust, and the skin mounts;
//   - back in Comic the gallery says it is in this browser;
//   - a tampered package is refused and nothing new is stored.
//   node skin_gallery_rendered.cjs DAEMON_URL_WITH_TOKEN
const path = require('path'), crypto = require('crypto'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
const LATEST = 'https://api.github.com/repos/misunders2d/agentnet-skins/releases/latest';
const RAW = 'https://raw.githubusercontent.com/misunders2d/agentnet-skins/';
const sha = (b) => crypto.createHash('sha256').update(b).digest();

function pkg(id, name, agentnet, word) {
  const m = { api: 1, id, name, entry: 'entry.mjs', style: 'style.css', files: ['entry.mjs', 'style.css'], ...(agentnet ? { agentnet } : {}) };
  return {
    'skin.json': Buffer.from(JSON.stringify(m)),
    'entry.mjs': Buffer.from(`export function mount(root,host){const h=document.createElement('h1');h.textContent=${JSON.stringify(word)};root.append(h);host.onOpen(()=>{});}export function unmount(root){root.replaceChildren();}`),
    'style.css': Buffer.from('h1 { font: 700 32px serif; }'),
  };
}
// The consent digest: the manifest's bytes, then each declared file's name, NUL and SHA-256 (local-skins.mjs).
function digest(files) {
  const m = JSON.parse(files['skin.json']);
  return crypto.createHash('sha256').update(Buffer.concat([files['skin.json'], ...m.files.flatMap((n) => [Buffer.from(n), Buffer.from([0]), sha(files[n])])])).digest('hex');
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    // A real PNG for the previews.
    const painter = await browser.newPage({ viewport: { width: 320, height: 200 } });
    await painter.setContent('<body style="margin:0;background:#F2E6C9"><div style="margin:40px;height:120px;background:#B3261E"></div></body>');
    const png = await painter.screenshot();
    await painter.close();

    const pulp = pkg('pulp', 'Pulp', 'v0.8.25', 'Pulp gallery skin'), pulpUpdate = pkg('pulp', 'Pulp', 'v0.8.25', 'Pulp updated gallery skin'), diner = pkg('diner', 'Diner', 'v99.0.0', 'Diner gallery skin');
    const entry = (id, name, files, agentnet) => ({ id, name, description: name + ' is a test skin.', version: '1.0.0', api: 1, agentnet, digest: digest(files),
      files: Object.entries(files).map(([p, b]) => ({ path: p, sha256: sha(b).toString('hex'), size: b.length })),
      previews: [{ path: 'previews/desktop-light.png', sha256: sha(png).toString('hex'), size: png.length, alt: name + ' on a desktop, light' },
        { path: 'previews/phone-dark.png', sha256: sha(png).toString('hex'), size: png.length, alt: name + ' on a phone, dark' }] });
    const catalog = { catalog: 1, skins: [entry('pulp', 'Pulp', pulp, 'v0.8.25'), entry('diner', 'Diner', diner, 'v99.0.0')] };
    const updatedCatalog = { catalog: 1, skins: [{ ...entry('pulp', 'Pulp', pulpUpdate, 'v0.8.25'), version: '1.1.0' }, entry('diner', 'Diner', diner, 'v99.0.0')] };

    for (const [tag, width, height, scheme] of [['desk-light', 1440, 960, 'light'], ['phone-dark', 390, 844, 'dark']]) {
      const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme });
      const page = await ctx.newPage(), errors = [], outside = [];
      let tamper = false, updatePublished = false;
      page.on('pageerror', (e) => errors.push(e.message));
      page.on('request', (r) => { const u = new URL(r.url()); if (u.protocol.startsWith('http') && u.origin !== new URL(target).origin && !r.url().startsWith(LATEST) && !r.url().startsWith(RAW)) outside.push(r.url()); });
      await ctx.route(LATEST, (route) => route.fulfill({ status: 200, contentType: 'application/json', headers: { 'Access-Control-Allow-Origin': '*' }, body: JSON.stringify({ tag_name: updatePublished ? 'v1.1.0' : 'v1.0.0', draft: false, prerelease: false }) }));
      await ctx.route(RAW + '**', (route) => {
        const updated = route.request().url().startsWith(RAW + 'v1.1.0/published/');
        const prefix = RAW + (updated ? 'v1.1.0' : 'v1.0.0') + '/published/', rest = route.request().url().slice(prefix.length), cors = { 'Access-Control-Allow-Origin': '*' };
        if (!route.request().url().startsWith(prefix)) return route.fulfill({ status: 404, headers: cors, body: '' });
        if (rest === 'catalog.json') return route.fulfill({ status: 200, headers: cors, contentType: 'text/plain', body: JSON.stringify(updated ? updatedCatalog : catalog) });
        const [id, ...p] = rest.split('/'), file = p.join('/');
        let body = file.startsWith('previews/') ? png : ({ pulp: updated ? pulpUpdate : pulp, diner }[id] || {})[file];
        if (!body) return route.fulfill({ status: 404, headers: cors, body: '' });
        if (tamper && file === 'entry.mjs') body = Buffer.concat([body, Buffer.from('\n// changed')]);
        return route.fulfill({ status: 200, headers: cors, contentType: 'text/plain', body });
      });
      // The demo program says version "demo", which never counts as old; stamp a release.
      await ctx.route(/\/api\/overview/, async (route) => { const r = await route.fetch(); const j = await r.json(); j.version = 'v0.8.25'; await route.fulfill({ response: r, json: j }); });
      const snap = (n) => shots && page.screenshot({ path: path.join(shots, 'gallery-' + tag + '-' + n + '.png'), fullPage: false });
      const origin = new URL(target).origin;
      const settings = async () => {
        await page.getByRole('heading', { name: 'Chats' }).first().waitFor({ timeout: 20000 });
        await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: /^(Settings|You)$/ }).click();
        await page.getByRole('button', { name: /Appearance/ }).first().click();
        await page.getByRole('heading', { name: 'Skin gallery', exact: true }).waitFor();
      };

      await page.goto(target);
      await page.evaluate(() => (localStorage.removeItem('agentnet.skin.package'), localStorage.setItem('agentnet.skin', 'comic')));
      await page.goto(origin + '/');
      await settings();
      const install = page.getByRole('button', { name: 'Install Pulp' });
      await install.waitFor({ timeout: 15000 });
      // Verified previews render; the incompatible skin cannot be installed.
      await page.getByRole('img', { name: 'Pulp on a desktop, light' }).waitFor();
      assert.ok(await page.getByRole('img', { name: 'Pulp on a desktop, light' }).evaluate((i) => i.complete && i.naturalWidth > 0 && i.src.startsWith('blob:')), tag + ': preview drawn from verified bytes');
      await page.getByRole('button', { name: 'Next picture of Pulp' }).click();
      await page.getByRole('img', { name: 'Pulp on a phone, dark' }).waitFor();
      const needs = page.getByRole('button', { name: 'Needs AgentNet v99.0.0' });
      assert.equal(await needs.isDisabled(), true, tag + ': incompatible skin cannot be installed');
      assert.equal(await page.getByRole('button', { name: 'Install Diner' }).count(), 0);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, tag + ': no horizontal scroll');
      await page.getByRole('heading', { name: 'Skin gallery', exact: true }).scrollIntoViewIfNeeded();
      await snap('list');

      // A tampered package is refused with nothing stored.
      tamper = true;
      await install.click();
      await page.getByRole('alert').filter({ hasText: 'did not match the gallery' }).waitFor();
      assert.equal(await page.evaluate(async () => (await caches.keys()).filter((k) => k.startsWith('agentnet-skin-package-v1-')).length), 0, tag + ': nothing stored after a mismatch');
      tamper = false;

      // Install → Use → trust → mounted.
      await install.click();
      const use = page.getByRole('dialog').getByRole('button', { name: 'Use Pulp' });
      await use.waitFor({ timeout: 15000 });
      await snap('use');
      await use.click();
      const trust = page.getByRole('button', { name: 'Use this skin' });
      await trust.waitFor({ timeout: 20000 });
      const card = await page.locator('.skin-card').boundingBox(), launcher = await page.getByRole('button', { name: 'AgentNet controls', exact: true }).boundingBox();
      assert(card && launcher && (Math.min(card.x+card.width,launcher.x+launcher.width)<=Math.max(card.x,launcher.x)||Math.min(card.y+card.height,launcher.y+launcher.height)<=Math.max(card.y,launcher.y)), 'host launcher must not cover the trust card');
      await snap('trust');
      await trust.evaluate(button => button.addEventListener('click', () => queueMicrotask(() => { window.skinTrustFeedback = { disabled: button.disabled, label: button.textContent }; }), { once: true }));
      await trust.click();
      assert.deepEqual(await page.evaluate(() => window.skinTrustFeedback), { disabled: true, label: 'Opening Pulp…' }, 'explicit trust gives immediate accurate opening feedback');
      await page.waitForFunction(() => { const sr = document.getElementById('skin').shadowRoot; return !!sr && /Pulp gallery skin/.test(sr.textContent); }, null, { timeout: 20000 });

      // Back to Comic: the gallery knows it is in this browser.
      await page.getByRole('button', { name: 'AgentNet controls', exact: true }).click();
      await page.getByRole('dialog', { name: 'AgentNet controls', exact: true }).getByRole('button', { name: 'Switch to Comic', exact: true }).click();
      await settings();
      await page.getByRole('button', { name: 'Use Pulp' }).first().waitFor({ timeout: 15000 });
      // The switcher's "Use Pulp" can appear before the gallery list has loaded again.
      await page.getByText('In this browser', { exact: true }).first().waitFor({ timeout: 15000 })
        .catch(() => assert.fail(tag + ': installed skin marked'));

      // A new published digest is an update, and needs its own exact trust.
      updatePublished = true;
      await page.reload();
      await settings();
      await page.getByRole('button', { name: 'Update Pulp', exact: true }).click();
      const useUpdate = page.getByRole('dialog').getByRole('button', { name: 'Use Pulp', exact: true });
      await useUpdate.waitFor({ timeout: 15000 });
      await snap('update-use');
      await useUpdate.click();
      const trustUpdate = page.getByRole('button', { name: 'Use this skin', exact: true });
      await trustUpdate.waitFor({ timeout: 20000 });
      assert.equal(await page.evaluate(() => localStorage.getItem('agentnet.skin.trusted.local:pulp')), digest(pulp), 'old consent cannot cover the update');
      await snap('update-trust');
      await trustUpdate.click();
      await page.waitForFunction(() => /Pulp updated gallery skin/.test(document.getElementById('skin').shadowRoot.textContent), null, { timeout: 20000 });
      assert.equal(await page.evaluate(() => localStorage.getItem('agentnet.skin.trusted.local:pulp')), digest(pulpUpdate), 'consent names the exact updated bytes');
      await page.getByRole('button', { name: 'AgentNet controls', exact: true }).click();
      await page.getByRole('dialog', { name: 'AgentNet controls', exact: true }).getByRole('button', { name: 'Switch to Comic', exact: true }).click();
      await settings();
      const updatedCard = page.locator('section[aria-labelledby="appearance-gallery"]').locator('article,div').filter({ has: page.getByRole('button', { name: 'Use Pulp', exact: true }) }).first();
      await updatedCard.getByText('1.1.0', { exact: true }).waitFor({ timeout: 15000 });
      await updatedCard.getByText('In this browser', { exact: true }).waitFor({ timeout: 15000 });
      if (tag === 'desk-light') {
        // A package imported by hand that needs a newer AgentNet is stored (import checks only the
        // field's form) but cannot be chosen: the host refuses and Comic says why.
        const later = { api: 1, id: 'later', name: 'Later', agentnet: 'v99.0.0', entry: 'entry.mjs', files: ['entry.mjs'] };
        await page.locator('input[aria-label="Import skin files"]').setInputFiles([
          { name: 'skin.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(later)) },
          { name: 'entry.mjs', mimeType: 'text/javascript', buffer: Buffer.from('export function mount(root,host){host.onOpen(()=>{});root.textContent="later";}') }]);
        await page.getByRole('button', { name: 'Use Later' }).click();
        await page.getByRole('dialog').getByRole('button', { name: 'Use Later' }).click();
        await page.getByText('This skin needs AgentNet v99.0.0 or newer. Update AgentNet first.').waitFor();
        assert.notEqual(new URL(page.url()).searchParams.get('skin'), 'local:later', tag + ': no switch to a skin that needs a newer AgentNet');
      }
      assert.deepEqual(outside, [], tag + ': no other external requests');
      assert.deepEqual(errors, [], tag + ': page errors');
      await ctx.close();
    }
    console.log('skin gallery rendered PASS: desktop light and phone dark — verified previews, version gate, tamper refused, install/update → exact digest trust → mounted → return to Comic, marked installed');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
