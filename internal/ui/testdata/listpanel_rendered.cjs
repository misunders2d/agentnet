// MEL-822/824 in Chromium on Comic: hide and show the desktop list column from
// its active rail item, resize it by keyboard and mouse within bounds, keep the
// open chat, its draft and attached file in the same mounted composer, bring
// it back for Ctrl K, remember width and hidden state, leave Agents full width and
// the phone layout unchanged. Each local change is measured to its next frame.
//   node listpanel_rendered.cjs DAEMON_URL_WITH_TOKEN
const path = require('path'), assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [target] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  const timings = [];
  try {
    for (const [tag, width, height, scheme] of [['laptop-light', 1280, 720, 'light'], ['small-dark', 1024, 700, 'dark'], ['desktop-light', 1920, 1080, 'light']]) {
      const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: scheme });
      const page = await ctx.newPage(), errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      const rail = page.getByRole('navigation', { name: 'Main' });
      const chats = rail.getByRole('button', { name: 'Chats', exact: true });
      const aside = page.locator('#an-list-column');
      const sep = page.getByRole('separator', { name: 'Resize the list' });
      // click → the expected state at the next frame, measured in the page.
      const timed = async (id, act, ok) => {
        const t0 = await page.evaluate(() => performance.now());
        await act();
        const ms = await page.evaluate(async ({ t0 }) => { await new Promise((r) => requestAnimationFrame(r)); return performance.now() - t0; }, { t0 });
        assert.ok(await ok(), tag + ': ' + id);
        assert.ok(ms < 1100, tag + ': ' + id + ' took ' + ms + ' ms');
        timings.push(tag + ' ' + id + ' ' + ms.toFixed(0) + 'ms');
      };
      await page.goto(target);
      const layout = page.getByRole('group', {name: 'Chat list layout'});
      const grouped = layout.getByRole('button', {name: 'Grouped'});
      const recent = layout.getByRole('button', {name: 'Recent'});
      const groups = page.getByRole('list', {name: 'Groups', exact: true});
      const groupHeader = page.getByRole('button', {name: /^Groups 2 unread$/});
      const dms = page.getByRole('list', {name: 'DMs', exact: true});
      const agentsList = page.getByRole('list', {name: 'Agents', exact: true});
      await groups.getByText('Ops group', {exact:true}).waitFor();
      await dms.locator('[data-part=chat-row]').filter({hasText: 'Fixture message'}).getByText('Bob', {exact:true}).waitFor();
      await agentsList.locator('[data-kind=agent]').first().waitFor();
      assert.equal(await grouped.getAttribute('aria-pressed'), 'true');
      await timed('collapse-group-section', () => groupHeader.click(), async () => (await groupHeader.getAttribute('aria-expanded')) === 'false' && !(await groups.isVisible()));
      const listSearch = page.getByPlaceholder('Search people, agents, groups');
      await timed('search-through-collapsed-section', () => listSearch.fill('Ops group'), async () => (await page.getByRole('list', {name: 'Matching chats'}).getByText('Ops group').isVisible()));
      await listSearch.press('ArrowDown');
      assert.equal(await page.getByRole('list', {name: 'Matching chats'}).locator('[data-part=chat-row]').evaluate((el) => el.getRootNode().activeElement === el), true);
      await listSearch.fill('');
      await timed('recent', () => recent.click(), async () => (await recent.getAttribute('aria-pressed')) === 'true' && !(await groups.isVisible()));
      await page.reload();
      await recent.waitFor();
      assert.equal(await recent.getAttribute('aria-pressed'), 'true', 'Recent choice survived reload');
      await timed('grouped', () => grouped.click(), async () => (await grouped.getAttribute('aria-pressed')) === 'true' && (await groups.isVisible()));
      await dms.locator('[data-part=chat-row]').filter({hasText: 'Fixture message'}).click();
      const field = page.locator('[data-part=composer] textarea').first();
      await field.waitFor({ timeout: 15000 });
      await page.locator('[data-part=composer] input[type=file]').first().setInputFiles({ name: 'panel-note.txt', mimeType: 'text/plain', buffer: Buffer.from('kept') });
      await field.fill('Draft kept while the list hides');
      await field.evaluate((el) => { el.__mel824 = true; });
      await page.locator('#an-list-column [data-part=chat-row]').first().waitFor();
      const before = await aside.boundingBox();
      assert.equal(Math.round(before.width), 320, tag + ': initial list width');
      if (shots) await page.screenshot({ path: path.join(shots, 'listpanel-' + tag + '-shown.png') });

      await timed('hide', () => chats.click(), async () => (await chats.getAttribute('aria-expanded')) === 'false' && (await aside.getAttribute('data-hidden')) === '' && (await aside.boundingBox()).width < 1);
      assert.equal(await page.locator('#an-list-column [inert]').count() + (await aside.evaluate((a) => a.inert ? 1 : 0)), 1, tag + ': hidden list is inert');
      assert.equal(await field.evaluate((el) => el.__mel824 === true && el.isConnected), true, tag + ': same composer, not remounted');
      assert.equal(await field.inputValue(), 'Draft kept while the list hides', tag + ': draft kept');
      await page.getByText('panel-note.txt').first().waitFor({ timeout: 3000 });
      if (shots) await page.screenshot({ path: path.join(shots, 'listpanel-' + tag + '-hidden.png') });
      await timed('show', () => chats.click(), async () => (await chats.getAttribute('aria-expanded')) === 'true' && (await aside.boundingBox()).width > 300);

      // Keyboard: 16px steps, Shift 48px, Home/End bounds.
      await sep.focus();
      await timed('key-right', () => page.keyboard.press('ArrowRight'), async () => (await sep.getAttribute('aria-valuenow')) === '336');
      await timed('key-shift-left', () => page.keyboard.press('Shift+ArrowLeft'), async () => (await sep.getAttribute('aria-valuenow')) === '288');
      await timed('key-home', () => page.keyboard.press('Home'), async () => (await sep.getAttribute('aria-valuenow')) === '240' && Math.round((await aside.boundingBox()).width) === 240);
      const max = Number(await sep.getAttribute('aria-valuemax'));
      assert.equal(max, Math.max(240, Math.min(480, width - 68 - 440)), tag + ': bound keeps 440px for the conversation');
      await timed('key-end', () => page.keyboard.press('End'), async () => Number(await sep.getAttribute('aria-valuenow')) === max);
      assert.ok(width - 68 - (await aside.boundingBox()).width >= 440, tag + ': conversation keeps its room');
      // Mouse: drag the edge left 100px, and past the lower bound.
      const box = await sep.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + 200);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width / 2 - 100, box.y + 200, { steps: 5 });
      const dragging = (await aside.boundingBox()).width;
      assert.ok(Math.abs(dragging - (max - 100)) <= 2, tag + ': follows the pointer while dragging (' + dragging + ')');
      await page.mouse.move(box.x - 600, box.y + 200, { steps: 3 });
      await page.mouse.up();
      assert.equal(Math.round((await aside.boundingBox()).width), 240, tag + ': stops at the lower bound');
      assert.equal(await sep.getAttribute('aria-valuenow'), '240');
      await timed('double-click-reset', () => sep.dblclick(), async () => (await sep.getAttribute('aria-valuenow')) === '320');
      assert.equal(await field.evaluate((el) => el.__mel824 === true && el.isConnected), true, tag + ': resizing kept the composer');

      // Settings opens its list; its rail item hides it; Chats opens the chat list again.
      const settings = rail.getByRole('button', { name: 'Settings', exact: true });
      await timed('settings-open', () => settings.click(), async () => (await settings.getAttribute('aria-current')) === 'page' && (await settings.getAttribute('aria-expanded')) === 'true');
      await timed('settings-hide', () => settings.click(), async () => (await settings.getAttribute('aria-expanded')) === 'false');
      await timed('chats-reopens', () => chats.click(), async () => (await chats.getAttribute('aria-expanded')) === 'true' && (await aside.boundingBox()).width > 300);
      await field.waitFor();
      assert.equal(await field.inputValue(), 'Draft kept while the list hides', tag + ': draft kept across sections');
      await page.getByText('panel-note.txt').first().waitFor({ timeout: 3000 });
      // Ctrl K finds a chat: a hidden chat list comes back with search focused.
      await timed('hide-again', () => chats.click(), async () => (await chats.getAttribute('aria-expanded')) === 'false');
      await field.focus();
      const search = page.getByPlaceholder('Search people, agents, groups');
      const k0 = Date.now();
      await page.keyboard.press('Control+k');
      await page.waitForFunction(() => { const r = document.getElementById('skin')?.shadowRoot || document; const a = r.activeElement; return a && a.placeholder === 'Search people, agents, groups' && !a.closest('[inert]'); }, null, { timeout: 1100 });
      timings.push(tag + ' ctrl-k-shows-search ' + (Date.now() - k0) + 'ms');
      assert.equal(await chats.getAttribute('aria-expanded'), 'true', tag + ': Ctrl K showed the list');
      assert.ok(await search.isVisible(), tag + ': search visible');
      await page.keyboard.press('Escape');
      // Agents stays a full-width workspace: a second click hides nothing.
      const agents = rail.getByRole('button', { name: 'Agents', exact: true });
      await agents.click(); await agents.click();
      assert.equal(await page.locator('#an-list-column').count(), 0, tag + ': Agents has no list column');
      assert.equal(await agents.getAttribute('aria-expanded'), null, tag + ': Agents is not a toggle');
      await chats.click();
      // Hidden state and width are remembered on this device.
      await sep.focus(); await page.keyboard.press('ArrowRight'); await chats.click();
      await page.reload();
      await rail.waitFor();
      await page.waitForFunction(() => document.getElementById('skin')?.shadowRoot?.querySelector('#an-list-column[data-hidden]'), null, { timeout: 15000 });
      await chats.click();
      assert.equal(await sep.getAttribute('aria-valuenow'), '336', tag + ': width remembered');
      assert.deepEqual(errors, [], tag + ': page errors');
      await ctx.close();
    }
    // Phone: tabs, no rail toggle and no separator; tapping Chats twice keeps the list.
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });
    const page = await ctx.newPage();
    await page.goto(target);
    const tabs = page.getByRole('navigation', { name: 'Main' });
    await tabs.getByRole('button', { name: 'Chats' }).tap(); await tabs.getByRole('button', { name: 'Chats' }).tap();
    assert.equal(await page.getByRole('separator', { name: 'Resize the list' }).count(), 0, 'phone: no separator');
    await page.locator('[data-part=chat-row]').first().waitFor();
    assert.equal(await page.getByRole('group', {name: 'Chat list layout'}).count(), 0, 'phone: chronological list');
    assert.equal(await page.locator('[data-part=rail]').count(), 0, 'phone: no rail');
    await ctx.close();
    console.log(timings.join('\n'));
    console.log('list panel rendered PASS: 1280, 1024 and 1920 desktop, 390 phone — hide/show from the active rail item, keyboard and mouse resize within bounds, composer, draft and file kept, remembered, Agents full width, phone unchanged');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
