// MEL-829/830 in Chromium: Settings → Coding agents → Desktop chats. Connect
// asks first (Cancel sends nothing), shows "Connecting…" at once while the
// backend takes 1.5 s, then "Connected as <name>"; a refused connect shows the
// reason and offers Try again; an empty name never reaches the backend; a chat
// that can't receive requests can't be connected; an already connected chat
// says so. Light and dark.
//   node desktopchats_rendered.cjs DAEMON_URL_WITH_TOKEN
const path = require('path'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  const timings = [];
  try {
    for (const scheme of ['light', 'dark']) {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: scheme });
      const page = await ctx.newPage(), errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      await page.goto(target);
      await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: 'Settings', exact: true }).click();
      await page.getByRole('button', { name: /^Coding agents/ }).click();
      const section = page.getByRole('region', { name: 'Desktop chats' });
      await section.waitFor();
      const row = (k) => section.locator(`[data-desktop-chat="${k}"]`);
      await row('claude:sess-ok').waitFor();
      assert.equal(await section.locator('[data-desktop-chat]').count(), 4, scheme + ': four chats');
      assert.ok(await row('codex:thread-done').getByText('Connected as Docs helper').isVisible(), scheme + ': connected chat says so');
      assert.equal(await row('codex:thread-done').getByRole('button').count(), 0, scheme + ': no Connect on a connected chat');
      assert.ok(await row('claude:sess-closed').getByRole('button', { name: 'Connect' }).isDisabled(), scheme + ': closed chat cannot connect');
      assert.ok(await row('claude:sess-closed').getByText(/can’t receive requests yet/).isVisible(), scheme + ': closed chat says why');
      if (shots) await page.screenshot({ path: path.join(shots, 'desktopchats-' + scheme + '-list.png'), fullPage: true });

      const dialog = page.getByRole('alertdialog');
      const name = dialog.getByLabel('Name in AgentNet');
      // Cancel: nothing sent.
      await row('claude:sess-ok').getByRole('button', { name: 'Connect' }).click();
      await dialog.waitFor();
      assert.equal(await name.inputValue(), 'site', scheme + ': name defaults to the folder');
      await dialog.getByRole('button', { name: 'Cancel' }).click();
      await dialog.waitFor({ state: 'hidden' });
      // Empty name: local error, nothing sent.
      await row('claude:sess-ok').getByRole('button', { name: 'Connect' }).click();
      await name.fill('');
      await dialog.getByRole('button', { name: 'Connect', exact: true }).click();
      await row('claude:sess-ok').getByRole('alert').getByText('Give it a name to connect it.').waitFor();
      // Connect, slow backend: pending at once, then connected.
      await row('claude:sess-ok').getByRole('button', { name: 'Try again' }).click();
      await name.fill('Site agent');
      if (shots) await page.screenshot({ path: path.join(shots, 'desktopchats-' + scheme + '-confirm.png') });
      const t0 = await page.evaluate(() => performance.now());
      await dialog.getByRole('button', { name: 'Connect', exact: true }).click();
      const pending = row('claude:sess-ok').getByRole('button', { name: 'Connecting…' });
      await pending.waitFor();
      const ms = await page.evaluate((t0) => performance.now() - t0, t0);
      assert.ok(ms < 1100, scheme + ': pending took ' + ms + ' ms');
      assert.ok(await pending.isDisabled(), scheme + ': pending cannot be clicked twice');
      timings.push(scheme + ' connect-pending ' + ms.toFixed(0) + 'ms');
      // Other rows stay usable while it runs.
      await row('codex:thread-fail').getByRole('button', { name: 'Connect' }).click();
      await dialog.waitFor();
      assert.equal(await name.inputValue(), 'Release notes', scheme + ': name defaults to the chat name');
      await dialog.getByRole('button', { name: 'Connect', exact: true }).click();
      await row('claude:sess-ok').getByText('Connected as Site agent').waitFor({ timeout: 8000 });
      // Refused: reason shown, retry offered, state not faked.
      await row('codex:thread-fail').getByRole('alert').getByText('Codex Desktop is not running on this computer.').waitFor({ timeout: 8000 });
      assert.ok(await row('codex:thread-fail').getByRole('button', { name: 'Try again' }).isEnabled(), scheme + ': retry offered');
      assert.equal(await row('codex:thread-fail').getByText(/Connected as/).count(), 0, scheme + ': failure is not shown as connected');
      if (shots) await page.screenshot({ path: path.join(shots, 'desktopchats-' + scheme + '-after.png'), fullPage: true });
      assert.deepEqual(errors, [], scheme + ': page errors');
      await ctx.close();
    }
    console.log(timings.join('\n'));
    console.log('desktop chats rendered PASS');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
