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
        hasTouch: phone, isMobile: phone, deviceScaleFactor: phone ? 3 : 1, reducedMotion: 'reduce'});
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
      const composerAa = page.locator('form[data-part="composer"]').getByRole('button', {name: 'Formatting', exact: true});
      const composerStrip = page.getByRole('group', {name: 'Formatting options', exact: true});
      // Composer layout: one frame with the draft full width above flat tools;
      // Aa swaps the phone tools for the formatting strip without moving Send
      // or losing the field's focus and selection; the desktop strip shows by
      // default and its closed/open choice survives a remount on this device.
      if (!base && count === 40) {
        const frame = page.locator('[data-part="composer-field"]');
        const aa = composerAa;
        const strip = page.getByRole('group', {name: 'Formatting options', exact: true});
        const send = page.locator('[data-part="send"]');
        const box = async l => { const b = await l.boundingBox(); return b && [b.x, b.y, b.width, b.height].map(Math.round).join(','); };
        const state = () => field.evaluate(el => ({from: el.selectionStart, to: el.selectionEnd, focused: el.getRootNode().activeElement === el}));
        const noSideScroll = () => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
        const layout = async () => {
          const [f, t] = await Promise.all([frame.boundingBox(), field.boundingBox()]);
          assert(t.width >= f.width - 4, 'draft spans the frame: field ' + t.width + ' of ' + f.width);
          assert(await noSideScroll(), 'no horizontal page scroll');
          return Math.round(t.width);
        };
        report.layout = {field: await layout()};
        // Typography (rendered, not tokens): draft text 16px+, textarea and
        // mirror share every metric, 1.5 line height, readable contrast for
        // the draft and its placeholder, and an unclipped Send label.
        const typography = async (where, spacing = false) => {
          await field.fill('');
          const t = await field.evaluate((el, spacing) => {
            const root = el.getRootNode(), form = el.closest('form');
            if (spacing && !root.querySelector('#typo-spacing')) {
              const style = document.createElement('style');
              style.id = 'typo-spacing'; // WCAG 1.4.12 text-spacing override
              style.textContent = 'form[data-part="composer"] *{line-height:1.5!important;letter-spacing:.12em!important;word-spacing:.16em!important}';
              root.append(style);
            }
            const mirror = el.parentElement.querySelector('[aria-hidden="true"]');
            const hint = mirror.querySelector('.text-muted');
            const canvas = document.createElement('canvas').getContext('2d', {willReadFrequently: true});
            const rgb = color => { canvas.clearRect(0, 0, 1, 1); canvas.fillStyle = '#000'; canvas.fillStyle = color; canvas.fillRect(0, 0, 1, 1); return [...canvas.getImageData(0, 0, 1, 1).data]; };
            const bg = node => { for (let n = node; n; n = n.parentElement || n.getRootNode().host) { const c = rgb(getComputedStyle(n).backgroundColor); if (c[3] === 255) return c; } return [255, 255, 255, 255]; };
            const lum = ([r, g, b]) => [r, g, b].map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
            const ratio = (fg, back) => { const a = lum(fg), b = lum(back); return Math.round((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) * 100) / 100; };
            const pick = s => ({size: s.fontSize, line: s.lineHeight, family: s.fontFamily, weight: s.fontWeight, spacing: s.letterSpacing});
            const send = form.querySelector('[data-part="send"]'), label = [...send.querySelectorAll('span')].find(s => getComputedStyle(s).display !== 'none' && s.textContent.trim());
            return {
              textarea: pick(getComputedStyle(el)), mirror: pick(getComputedStyle(mirror)),
              draftContrast: ratio(rgb(getComputedStyle(mirror).color), bg(mirror)),
              placeholderContrast: hint ? ratio(rgb(getComputedStyle(hint).color), bg(mirror)) : null,
              placeholderClipped: hint ? hint.scrollWidth > hint.clientWidth : null,
              sendLabel: label ? {size: getComputedStyle(label).fontSize, clipped: label.scrollWidth > label.clientWidth, contrast: ratio(rgb(getComputedStyle(label).color), bg(send))} : null,
              rowOverflow: form.querySelector('[data-part="composer-tools"]').scrollWidth > form.querySelector('[data-part="composer-tools"]').clientWidth + 1,
              pageOverflow: document.documentElement.scrollWidth > innerWidth,
            };
          }, spacing);
          if (!spacing) {
            assert.deepEqual(t.textarea, t.mirror, where + ': textarea and mirror metrics match');
            assert(parseFloat(t.textarea.size) >= 16, where + ': draft text ' + t.textarea.size);
            assert(parseFloat(t.textarea.line) / parseFloat(t.textarea.size) >= 1.45, where + ': draft line height ' + t.textarea.line);
          }
          assert(t.draftContrast >= 4.5, where + ': draft contrast ' + t.draftContrast);
          assert(t.placeholderContrast === null || t.placeholderContrast >= 4.5, where + ': placeholder contrast ' + t.placeholderContrast);
          if (t.sendLabel) assert(!t.sendLabel.clipped && parseFloat(t.sendLabel.size) >= 16 && t.sendLabel.contrast >= 4.5, where + ': Send label ' + JSON.stringify(t.sendLabel));
          assert(!t.rowOverflow && !t.pageOverflow, where + ': no overflow ' + JSON.stringify(t));
          if (t.sendLabel) {
            // Ready Send (act ink on act) as well as the idle one measured above.
            await field.fill('ready');
            t.readySend = await page.locator('form[data-part="composer"] [data-part="send"]').evaluate(send => {
              const label = [...send.querySelectorAll('span')].find(s => getComputedStyle(s).display !== 'none' && s.textContent.trim());
              const c = document.createElement('canvas').getContext('2d', {willReadFrequently: true});
              const rgb = color => { c.clearRect(0, 0, 1, 1); c.fillStyle = color; c.fillRect(0, 0, 1, 1); return [...c.getImageData(0, 0, 1, 1).data]; };
              const lum = ([r, g, b]) => [r, g, b].map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
              const a = lum(rgb(getComputedStyle(label).color)), b = lum(rgb(getComputedStyle(send).backgroundColor));
              return {clipped: label.scrollWidth > label.clientWidth, contrast: Math.round((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) * 100) / 100};
            });
            assert(!t.readySend.clipped && t.readySend.contrast >= 4.5, where + ': ready Send ' + JSON.stringify(t.readySend));
            await field.fill('');
          }
          (report.typography ||= {})[where] = t;
        };
        await typography(phone ? 'phone 390' : 'desktop 1440');
        if (!phone) {
          // 200% zoom of a 1280x900 window: 640x450 CSS px (phone layout, short window).
          await page.setViewportSize({width: 640, height: 450});
          await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
          await typography('200% of 1280x900');
          await page.setViewportSize({width: 1440, height: 900});
          await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
          await typography('desktop 1440 text spacing', true);
          await field.evaluate(el => el.getRootNode().querySelector('#typo-spacing')?.remove());
        }
        await field.fill('sample');
        await field.fill('sample');
        await select(field, 0);
        if (phone) {
          for (const width of [320, 390]) {
            await page.setViewportSize({width, height: 844});
            await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
            report.layout['field ' + width] = await layout();
            await field.focus();
            await field.evaluate(el => el.setSelectionRange(0, 6));
            const before = await box(send), clock = await box(page.getByRole('button', {name: 'Schedule…', exact: true}));
            // Closed: every tool fits left of the clock, nothing overlaps (3x DPR).
            const [emoji, clockBox] = await Promise.all([page.locator('form[data-part="composer"]').getByRole('button', {name: 'Emoji', exact: true}).boundingBox(),
              page.getByRole('button', {name: 'Schedule…', exact: true}).boundingBox()]);
            assert(emoji.x + emoji.width <= clockBox.x + 0.5, 'tools stay left of the clock at ' + width + ': ' + JSON.stringify([emoji, clockBox]));
            assert.equal(await aa.getAttribute('aria-expanded'), 'false');
            await measure('phone Aa ' + width, () => aa.tap(), strip);
            assert.equal(await aa.getAttribute('aria-expanded'), 'true');
            assert.equal(await page.getByRole('button', {name: 'Attach a file', exact: true}).isVisible(), false, 'strip replaces the tools');
            assert.equal(await box(send), before, 'Send stays in place');
            assert.equal(await box(page.getByRole('button', {name: 'Schedule…', exact: true})), clock, 'clock stays in place');
            assert.deepEqual(await state(), {from: 0, to: 6, focused: true}, 'Aa keeps focus and selection');
            const s = await strip.boundingBox(), sb = await send.boundingBox();
            assert(s.x + s.width <= sb.x, 'strip stays left of Send');
            assert(Math.abs(s.y + s.height / 2 - (sb.y + sb.height / 2)) < 2, 'strip sits in the tools row');
            assert(await noSideScroll(), 'strip scrolls inside the row, not the page');
            if (screenshots) {
              fs.mkdirSync(screenshots, {recursive: true, mode: 0o700});
              await page.screenshot({path: path.join(screenshots, 'composer-phone-' + width + '-format.png')});
            }
            await mark();
            await strip.getByRole('button', {name: 'Bold', exact: true}).tap();
            assert.equal(await field.inputValue(), '**sample**', 'strip formats the selection');
            assert.equal((await state()).focused, true, 'strip keeps the field focused');
            await record('phone strip Bold ' + width, field);
            await aa.tap();
            assert.equal(await page.getByRole('button', {name: 'Attach a file', exact: true}).isVisible(), true);
            assert.equal(await box(send), before, 'Send stays in place after closing');
            assert.equal((await state()).focused, true);
            await field.fill('sample');
          }
          // A refused clock (this fixture's host has no scheduling) says why.
          const refusedClock = page.getByRole('button', {name: 'Schedule…', exact: true});
          assert.equal(await refusedClock.getAttribute('aria-disabled'), 'true');
          await measure('phone refused clock', () => refusedClock.tap({force: true}), page.getByRole('alert').filter({hasText: 'Scheduled sending is unavailable on this host.'}));
          assert.equal(await field.inputValue(), 'sample', 'refused clock keeps the draft');
        } else {
          const above = async () => (await strip.boundingBox()).y < (await field.boundingBox()).y;
          assert.equal(await aa.getAttribute('aria-expanded'), 'true', 'desktop formatting open by default');
          assert(await above(), 'strip sits above the draft');
          await select(field, 0);
          await mark();
          await strip.getByRole('button', {name: 'Italic', exact: true}).click();
          assert.equal(await field.inputValue(), '*sample*');
          assert.deepEqual(await state(), {from: 1, to: 7, focused: true}, 'strip keeps focus and selection');
          await record('desktop strip Italic', field);
          const remount = async () => { await page.reload(); await chat.click(); await field.waitFor(); };
          await measure('desktop Aa close', () => aa.click(), page.locator('form[data-part="composer"] [aria-label="Formatting"][aria-expanded="false"]'));
          assert.equal(await strip.count(), 0);
          await remount();
          assert.equal(await aa.getAttribute('aria-expanded'), 'false', 'closed choice survives a remount');
          assert.equal(await strip.count(), 0);
          await aa.click();
          await remount();
          assert.equal(await aa.getAttribute('aria-expanded'), 'true', 'open choice survives a remount');
          assert(await above());
          const options = page.getByRole('button', {name: 'More send options', exact: true});
          const [sb, ob] = await Promise.all([send.boundingBox(), options.boundingBox()]);
          assert(sb.x + sb.width <= ob.x + 0.1 && sb.width >= 52, 'Send and its menu stay separate');
          await measure('desktop Send menu', () => options.click(), page.getByRole('menuitem', {name: /^Schedule…/}));
          await page.keyboard.press('Escape');
          await field.fill('');
        }
      }
      // Returns the chosen formatting action: a menu item, or on a phone's
      // composer a button in the Aa strip (Aa replaced the selection button).
      const menus = async (target, scope = page) => {
        if (phone && scope === page) {
          if (await composerAa.getAttribute('aria-expanded') !== 'true')
            await measure('touch formatting strip', () => composerAa.tap(), composerStrip.getByRole('button', {name: 'Bold', exact: true}));
          assert.equal(await composerAa.getAttribute('aria-expanded'), 'true');
          assert.equal(await page.getByRole('menuitem', {name: 'Bold', exact: true}).count(), 0, 'no selection menu beside the strip');
          return name => composerStrip.getByRole('button', {name, exact: true});
        }
        const submenu = phone ? scope.getByRole('button', {name: 'Formatting', exact: true}) : page.getByRole('menuitem', {name: 'Formatting', exact: true});
        if (!phone) {
          await measure('context menu', () => target.click({button: 'right'}), submenu);
          await measure('formatting submenu', () => submenu.click(), page.getByRole('menuitem', {name: 'Bold', exact: true}));
        } else {
          await submenu.waitFor();
          await measure('touch formatting menu', () => submenu.tap(), page.getByRole('menuitem', {name: 'Bold', exact: true}));
        }
        return name => page.getByRole('menuitem', {name, exact: true});
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
          const pick = await menus(field);
          if (screenshots && count === 40 && name === 'Bold') {
            fs.mkdirSync(screenshots, {recursive: true, mode: 0o700});
            await page.screenshot({path: path.join(screenshots, 'formatting-' + (phone ? 'touch' : 'desktop') + '.png')});
          }
          const choice = pick(name);
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
        const link = (await menus(field))('Link');
        await (phone ? link.tap() : link.click());
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
          const underline = (await menus(editor, row))('Underline');
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
