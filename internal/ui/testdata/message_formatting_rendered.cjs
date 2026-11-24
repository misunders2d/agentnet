const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
const base = !!process.env.AGENTNET_FORMATTING_BASE;
const screenshots = process.env.AGENTNET_SCREENSHOTS;
const operations = [
  ['Bold', 'Control+b', '**sample**'],
  ['Italic', 'Control+i', '*sample*'],
  ['Underline', 'Control+u', '^^sample^^'],
  ['Strikethrough', 'Control+Shift+x', '~~sample~~'],
  ['Monospace', 'Control+Shift+m', '`sample`'],
  ['Quote', 'Control+Shift+Period', '> sample'],
  ['Hidden text', 'Control+Shift+p', '||sample||'],
  ['Link', 'Control+k', '[sample](<https://example.test/link>)'],
  ['Clear formatting', 'Control+Shift+n', 'sample'],
];

(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',
    args: ['--no-sandbox', '--disable-dev-shm-usage']});
  const results = [];
  try {
    for (const count of [40, 400]) for (const phone of [false, true]) {
      const context = await browser.newContext({viewport: phone ? {width: 390, height: 844} : {width: 1440, height: 900},
        hasTouch: phone, isMobile: phone, reducedMotion: 'reduce'});
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', error => errors.push(String(error)));
      page.setDefaultTimeout(4500);
      const url = new URL(process.argv[2]);
      await context.request.post(new URL('/fixture-formatting/seed?count=' + count, url).href);
      await page.goto(url.href);
      const chat = page.getByRole('button', {name: /^Bob/}).first();
      await chat.click();
      const field = page.locator('[data-part="composer-field"] textarea');
      await field.waitFor();
      const report = {count, viewport: phone ? 'touch 390' : 'desktop 1440', ms: {}};
      results.push(report);
      async function mark() {
        await page.evaluate(() => {
          window.formattingAt = 0;
          const types = ['pointerdown', 'contextmenu', 'keydown', 'input'];
          const capture = event => {
            if (!event.isTrusted) return;
            window.formattingAt = performance.now();
            for (const type of types) window.removeEventListener(type, capture, true);
          };
          for (const type of types) window.addEventListener(type, capture, true);
        });
      }
      async function record(name, locator) {
        const result = await locator.evaluate(() => new Promise(resolve =>
          requestAnimationFrame(() => resolve(window.formattingAt ? performance.now() - window.formattingAt : -1))));
        assert(result >= 0 && result < 1100, name + ' feedback ' + result + ' ms');
        report.ms[name] = Math.round(result);
      }
      async function measure(name, input, visible) {
        await mark();
        await input();
        await visible.waitFor();
        await record(name, visible);
      }
      const select = async (target, start, end) => {
        await target.focus();
        if (start === 0 && end === undefined) await target.press('Control+a');
        else await target.evaluate((el, [from, to]) => el.setSelectionRange(from, to), [start, end]);
      };
      const menus = async target => {
        const submenu = page.getByRole(phone ? 'button' : 'menuitem', {name: 'Formatting', exact: true});
        if (!phone) {
          await measure('context menu', () => target.click({button: 'right'}), submenu);
          await measure('formatting submenu', () => submenu.click(), page.getByRole('menuitem', {name: 'Bold', exact: true}));
        } else {
          await submenu.waitFor();
          await measure('touch formatting menu', () => submenu.tap(), page.getByRole('menuitem', {name: 'Bold', exact: true}));
        }
      };
      const old = page.locator('[data-mid="history-' + (count - 2) + '"]');
      assert.equal(await old.locator('strong').textContent(), 'old bold');
      assert.equal(await old.locator('em').textContent(), 'old italic');
      assert.equal(await old.locator('code').textContent(), '||literal code||');
      const received = page.locator('[data-mid="history-' + (count - 1) + '"]');
      if (!base) {
        if (!phone) {
          await field.fill('sample');
          await select(field, 0);
          const browserMenu = await field.evaluate(target => target.dispatchEvent(new MouseEvent('contextmenu', {
            bubbles: true, composed: true, cancelable: true, shiftKey: true, button: 2,
          })));
          assert(browserMenu, 'Shift+right-click keeps the browser edit menu available');
          await field.fill('');
        }
        const code = page.locator('[data-mid="history-' + (count - 3) + '"]');
        assert.equal(await code.locator('pre code').textContent(), 'test -f a || exit 1\n\ntest -f b || exit 2');
        await code.getByRole('button', {name: '[Hidden text]', exact: true}).waitFor();
        assert(!String(await code.textContent()).includes('code-neighbor secret'));
        const spoiler = received.getByRole('button', {name: '[Hidden text]', exact: true});
        await spoiler.waitFor();
        assert.equal(await page.getByText('second secret', {exact: false}).count(), 0);
        if (!phone) assert(!String(await chat.textContent()).includes('outer'));
        const after = phone ? () => spoiler.tap() : () => spoiler.click();
        await measure('spoiler reveal', after, received.getByText(/second secret/));
        const revealed = received.getByRole('button', {name: /second secret/});
        await measure('spoiler conceal', () => revealed.press('Enter'), spoiler);
        const replyMenu = received.locator('[data-message-bubble]').first();
        if (!phone && await replyMenu.count()) {
          await replyMenu.click({button: 'right'});
          await page.getByRole('dialog').getByRole('button', {name: 'Reply', exact: true}).click();
          const chip = page.getByRole('button', {name: 'Cancel reply'});
          await chip.waitFor();
          assert(!String(await chip.evaluate(el => el.parentElement.textContent)).includes('outer'));
          await chip.click();
        }
        for (const [name, shortcut, expected] of operations) {
          const prepare = async () => {
            await field.fill(name === 'Clear formatting' ? '**sample**' : 'sample');
            await select(field, 0);
          };
          const finish = async label => {
            if (name === 'Link') {
              const dialog = page.getByRole('dialog', {name: 'Add link'});
              await dialog.waitFor();
              await record(label, dialog);
              const input = dialog.getByRole('textbox', {name: 'Link URL'});
              await input.fill('https://example.test/link');
              await mark();
              await dialog.getByRole('button', {name: 'Apply link'}).click();
            }
            assert.equal(await field.inputValue(), expected, label + ' updates retained draft immediately');
            await record(name === 'Link' ? label + ' apply' : label, field);
          };
          await prepare();
          await menus(field);
          if (screenshots && count === 40 && name === 'Bold') {
            fs.mkdirSync(screenshots, {recursive: true, mode: 0o700});
            await page.screenshot({path: path.join(screenshots, 'formatting-' + (phone ? 'touch' : 'desktop') + '.png')});
          }
          const choice = page.getByRole('menuitem', {name, exact: true});
          await mark();
          if (phone) await choice.tap(); else await choice.click();
          await finish('menu ' + name);
          if (!phone) {
            await prepare();
            await mark();
            await field.press(shortcut);
            await finish('key ' + name);
            const alias = {Monospace: 'm', 'Hidden text': 'p', 'Clear formatting': 'n'}[name];
            if (alias) {
              await prepare();
              await mark();
              await field.press('Control+Alt+' + alias);
              await finish('browser alias ' + name);
            }
          }
        }
        await field.fill('cancel link');
        await select(field, 0);
        await menus(field);
        await page.getByRole('menuitem', {name: 'Link', exact: true}).click();
        const dialog = page.getByRole('dialog', {name: 'Add link'});
        await dialog.getByRole('textbox', {name: 'Link URL'}).fill('javascript:alert(1)');
        assert.equal(await dialog.getByRole('button', {name: 'Apply link'}).isDisabled(), true);
        await mark();
        await dialog.getByRole('button', {name: 'Cancel', exact: true}).click();
        await dialog.waitFor({state: 'hidden'});
        assert.equal(await field.inputValue(), 'cancel link');
        await record('cancel link', field);
      }
      await field.fill('');
      const synced = page.waitForResponse(async response => new URL(response.url()).pathname === '/api/dm'
        && response.ok() && (await response.json()).messages.some(message => message.id === 'sync-row'));
      const sync = context.request.post(new URL('/fixture-formatting/sync', url).href);
      await mark();
      await field.press('s');
      assert.equal(await field.inputValue(), 's');
      await record('type during sync', field);
      await sync;
      await synced;
      assert.equal(await field.inputValue(), 's', 'sync leaves typed text intact');
      if (phone && !base) {
        await received.locator('[data-message-bubble]').first().click({button: 'right'});
        await page.getByRole('dialog').getByRole('button', {name: 'Reply', exact: true}).tap();
      }
      const body = '**sent formatting** ||private outgoing||';
      await field.fill(body);
      const response = page.waitForResponse(r => r.url().endsWith('/api/dm/send'));
      const sent = page.getByText('sent formatting', {exact: true});
      const send = page.getByRole('button', {name: 'Send', exact: true});
      await measure('pending send', () => send.click(), sent);
      const delivered = await response;
      assert.equal(delivered.request().postDataJSON().body, body);
      report.ms['send confirmed'] = Math.round(await page.evaluate(() => performance.now() - window.formattingAt));
      assert(report.ms['send confirmed'] >= 1150);
      const id = await sent.evaluate(el => el.closest('[data-mid]').dataset.mid);
      const row = page.locator('[data-mid=' + JSON.stringify(id) + ']');
      if (!base) assert.equal(await row.getByText('private outgoing', {exact: false}).count(), 0);
      if (!phone || !base) {
        await row.locator('[data-message-bubble]').first().click({button: 'right'});
        await page.getByRole('dialog').getByRole('button', {name: 'Edit', exact: true}).click();
        const editor = page.getByRole('textbox', {name: 'New text'});
        assert.equal(await editor.inputValue(), body);
        const updated = '^^edit exact^^';
        await editor.fill(base ? updated : 'edit exact');
        if (!base) {
          await select(editor, 0);
          if (phone) {
            const quote = row.getByRole('button', {name: /Before.*Hidden text/});
            const trigger = row.getByRole('button', {name: 'Formatting', exact: true});
            await trigger.waitFor();
            const [q, f] = await Promise.all([quote.boundingBox(), trigger.boundingBox()]);
            assert(q && f && f.y >= q.y + q.height - 1, 'touch formatting keeps the quoted reply unobstructed');
          }
          await menus(editor);
          const underline = page.getByRole('menuitem', {name: 'Underline', exact: true});
          await (phone ? underline.tap() : underline.click());
          assert.equal(await editor.inputValue(), updated);
        }
        if (!base && count === 40) {
          await context.request.post(new URL('/fixture-formatting/reject-edit', url).href);
          await page.getByRole('button', {name: 'Save', exact: true}).click();
          const again = page.getByRole('button', {name: 'Edit again', exact: true});
          await again.waitFor();
          await again.click();
          assert.equal(await editor.inputValue(), updated, 'a refused edit keeps its exact formatting for retry');
        }
        const saved = page.waitForResponse(r => r.url().endsWith('/api/message/edit'));
        const rendered = base ? row.getByText(/edit exact/).first() : row.locator('u').filter({hasText: 'edit exact'});
        await measure('pending edit', () => page.getByRole('button', {name: 'Save', exact: true}).click(), rendered);
        const confirmed = await saved;
        assert.equal(confirmed.request().postDataJSON().text, updated);
        report.ms['edit confirmed'] = Math.round(await page.evaluate(() => performance.now() - window.formattingAt));
        assert(report.ms['edit confirmed'] >= 1150);
      }
      assert.deepEqual(errors, []);
      await context.close();
    }
    console.log(JSON.stringify({base, results}));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
