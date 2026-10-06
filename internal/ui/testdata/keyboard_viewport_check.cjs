// MEL-519 as the messenger renders it (keyboard_rendered_test.go): a
// phone's keyboard never hides the message box. Android shrinks the page
// with the keyboard (a smaller viewport here); iOS shrinks only the visual
// viewport (a stand-in visualViewport here). Needs AGENTNET_PLAYWRIGHT;
// optional AGENTNET_SCREENSHOTS (a directory outside the repository).
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.KEYBOARD_URL, total = Number(process.env.KEYBOARD_TOTAL);
const origin = new URL(url).origin;
const shots = process.env.AGENTNET_SCREENSHOTS || '';
const KEYBOARD = 336, PHONE = { width: 390, height: 844 };

// In the page before any script: every focus() that moves the cursor into
// a text field (one already there is not counted), with whether it ran in a
// microtask (after the tap's own handler returned: too late for iOS to open
// its keyboard), whether it ran inside a click's own dispatch at all (from
// the window's capture listener to its bubble listener: not a later frame
// or timer) and whether it asked not to scroll. (window.event is never set
// for events in a shadow tree.)
function record() {
  const queue = window.queueMicrotask.bind(window);
  let micro = 0, tap = false;
  window.queueMicrotask = (cb) => queue(() => { micro++; try { cb(); } finally { micro--; } });
  addEventListener('click', () => { tap = true; setTimeout(() => { tap = false; }); }, true);
  addEventListener('click', () => { tap = false; });
  const focus = HTMLTextAreaElement.prototype.focus;
  window.__focus = [];
  HTMLTextAreaElement.prototype.focus = function (o) {
    if (this.getRootNode().activeElement !== this) window.__focus.push({ micro: micro > 0, tap, preventScroll: !!(o && o.preventScroll) });
    return focus.call(this, o);
  };
}

// iOS: the keyboard shrinks only the visual viewport, never the page.
function iosViewport() {
  const vv = new EventTarget();
  let kb = 0;
  Object.defineProperties(vv, {
    height: { get: () => innerHeight - kb }, width: { get: () => innerWidth }, scale: { get: () => 1 },
    offsetTop: { get: () => 0 }, offsetLeft: { get: () => 0 }, pageTop: { get: () => scrollY }, pageLeft: { get: () => scrollX },
  });
  Object.defineProperty(window, 'visualViewport', { configurable: true, get: () => vv });
  window.__keyboard = (px) => { kb = px; vv.dispatchEvent(new Event('resize')); };
}

// What is on screen now, in the open conversation.
const measure = (p) => p.evaluate(() => {
  const sr = document.getElementById('skin').shadowRoot;
  const scope = sr.querySelector('[data-card]:not([aria-hidden])') || sr.querySelector('main') || sr;
  const form = scope.querySelector('form[aria-label="Write a message"]'), field = form && form.querySelector('textarea');
  const log = scope.querySelector('[role="log"]'), box = log && log.parentElement;
  const sheet = sr.querySelector('.an-sheet');
  const css = document.documentElement.style;
  return {
    visible: window.visualViewport.height, html: document.documentElement.getBoundingClientRect().height,
    form: form ? Math.round(form.getBoundingClientRect().bottom) : null, field: field ? Math.round(field.getBoundingClientRect().bottom) : null,
    gap: box ? Math.round(box.scrollHeight - box.scrollTop - box.clientHeight) : null, msgs: log ? log.querySelectorAll('[data-mid]').length : 0,
    focused: !!field && sr.activeElement === field, sheet: sheet ? [Math.round(sheet.getBoundingClientRect().top), Math.round(sheet.getBoundingClientRect().bottom)] : null,
    vh: css.getPropertyValue('--an-viewport-h'), kb: css.getPropertyValue('--an-keyboard'), coarse: matchMedia('(pointer: coarse)').matches,
    focus: window.__focus.splice(0),
  };
});
const frames = (p, n = 3) => p.evaluate((n) => new Promise((done) => { const go = () => (n-- > 0 ? requestAnimationFrame(go) : done()); go(); }), n);

async function openChat(p, name) {
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', { timeout: 20000 });
  await p.getByRole('button', { name: /Not linked to a person/ }).first().click();
  await p.waitForFunction((n) => {
    const sr = document.getElementById('skin').shadowRoot;
    const scope = sr.querySelector('[data-card]:not([aria-hidden])') || sr.querySelector('main') || sr;
    const log = scope.querySelector('[role="log"]');
    return !!log && log.querySelectorAll('[data-mid]').length >= n && !!scope.querySelector('form[aria-label="Write a message"] textarea');
  }, total, { timeout: 20000 }).catch(async (e) => {
    await snap(p, 'failed-open-' + name.replace(/ /g, '-'));
    throw new Error(name + ': the conversation did not open with ' + total + ' messages and a message box: ' + JSON.stringify(await measure(p)) + '\n' + e.message);
  });
  await p.waitForTimeout(450); // the card has arrived
  const m = await measure(p);
  assert.ok(m.gap !== null && m.gap <= 1, name + ': the conversation opens at its newest message: ' + JSON.stringify(m));
  return m;
}
const snap = async (p, what) => { if (shots) await p.screenshot({ path: shots + '/keyboard-' + what + '.png' }); };
const field = (p) => p.locator('form[aria-label="Write a message"] textarea').last();

async function android(browser, scheme) {
  const name = 'android ' + scheme;
  const ctx = await browser.newContext({ viewport: PHONE, isMobile: true, hasTouch: true, colorScheme: scheme });
  await ctx.addInitScript(record);
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  let m = await openChat(p, name);
  assert.ok(m.coarse, name + ': the phone has a coarse pointer');
  assert.equal(m.focused, false, name + ': opening a chat on a phone does not open the keyboard');
  assert.deepEqual(m.focus, [], name + ': nothing focused the field on open');

  // Agent messages are read-only questions; no up-front execution toggle.
  assert.equal(await p.getByRole('radio', { name: 'Do it' }).count(), 0, name + ': no task toggle');
  assert.equal(await p.getByRole('radio', { name: 'Answer', exact: true }).count(), 0, name + ': no answer toggle');

  // Typing, the keyboard opens: the page shrinks above it (resizes-content).
  await field(p).tap();
  await field(p).fill('Count dock 3 again');
  await measure(p);
  await p.setViewportSize({ width: PHONE.width, height: PHONE.height - KEYBOARD });
  await frames(p);
  m = await measure(p);
  assert.ok(m.focused, name + ': still typing');
  assert.ok(m.form <= PHONE.height - KEYBOARD, name + ': the message box sits above the keyboard: ' + JSON.stringify(m));
  assert.ok(m.gap <= 1, name + ': the newest message stays in view: ' + JSON.stringify(m));
  assert.equal(m.vh + m.kb, '', name + ': the page itself shrank, so the host sets nothing');
  await snap(p, 'android-' + scheme + '-typing');

  // Continued typing keeps the keyboard and cursor in place.
  m = await measure(p);
  assert.ok(m.focused, name + ': typing retains the cursor');
  assert.ok(m.focus.every((f) => !f.preventScroll), name + ': no preventScroll on a touch screen: ' + JSON.stringify(m.focus));
  assert.ok(m.form <= PHONE.height - KEYBOARD && m.gap <= 1, name + ': still above the keyboard, newest in view: ' + JSON.stringify(m));

  // Reply on an approval card (the task waiting for this person's OK) puts
  // the cursor in this conversation's own field inside the tap, also when
  // the person was not typing (the keyboard closed).
  await field(p).evaluate((ta) => ta.blur());
  await p.setViewportSize(PHONE);
  await frames(p);
  await measure(p);
  await p.getByRole('button', { name: 'Answer it yourself' }).tap();
  m = await measure(p);
  assert.ok(m.focused, name + ': the card’s Reply puts the cursor in this conversation’s field: ' + JSON.stringify(m));
  assert.deepEqual(m.focus, [{ micro: false, tap: true, preventScroll: false }], name + ': the card’s Reply focuses inside the tap, scrolling the field into view');
  assert.ok(await p.getByText(/^Answering /).isVisible(), name + ': the card’s reply shows (answering the task by hand)');
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

async function ios(browser, scheme) {
  const name = 'ios ' + scheme;
  const ctx = await browser.newContext({ viewport: PHONE, isMobile: true, hasTouch: true, colorScheme: scheme });
  await ctx.addInitScript(record);
  await ctx.addInitScript(iosViewport);
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  await openChat(p, name);
  const visible = PHONE.height - KEYBOARD;

  // The keyboard opens over the page: the host fits the page to what is left.
  await field(p).tap();
  await p.evaluate((px) => window.__keyboard(px), KEYBOARD);
  await frames(p);
  let m = await measure(p);
  assert.equal(m.vh, visible + 'px', name + ': --an-viewport-h: ' + JSON.stringify(m));
  assert.equal(m.kb, KEYBOARD + 'px', name + ': --an-keyboard: ' + JSON.stringify(m));
  assert.equal(m.html, visible, name + ': the page is as tall as what the keyboard leaves');
  assert.ok(m.form <= visible, name + ': the message box sits above the keyboard: ' + JSON.stringify(m));
  assert.ok(m.gap <= 1, name + ': the newest message stays in view: ' + JSON.stringify(m));
  await snap(p, 'ios-' + scheme + '-typing');

  // A sheet sits on the keyboard (its inputs stay visible).
  await p.getByRole('button', { name: 'Message actions' }).last().evaluate((b) => b.click());
  await p.getByRole('dialog').waitFor();
  await p.waitForTimeout(400); // slid up
  m = await measure(p);
  assert.ok(m.sheet && m.sheet[1] <= visible + 1 && m.sheet[0] >= 0, name + ': the sheet sits above the keyboard: ' + JSON.stringify(m));
  await snap(p, 'ios-' + scheme + '-sheet');

  // The sheet took the focus, so the keyboard went away. Reply puts the
  // cursor back in the field within the tap itself (iOS opens its keyboard
  // only then), not in a later microtask or frame.
  await p.evaluate(() => window.__keyboard(0));
  await measure(p);
  await p.getByRole('dialog').getByRole('button', { name: 'Reply', exact: true }).tap();
  await p.waitForTimeout(600); // the sheet is gone, and nothing took the focus back
  m = await measure(p);
  assert.ok(m.focused, name + ': Reply puts the cursor in the field: ' + JSON.stringify(m));
  assert.deepEqual(m.focus, [{ micro: false, tap: true, preventScroll: false }], name + ': Reply focuses inside the tap, scrolling the field into view');
  assert.equal(await p.getByRole('dialog').count(), 0, name + ': the sheet closed');
  assert.ok(await p.getByText(/^Replying to/).isVisible(), name + ': the reply shows');
  await p.evaluate((px) => window.__keyboard(px), KEYBOARD);
  await frames(p);
  m = await measure(p);
  assert.ok(m.form <= visible && m.gap <= 1, name + ': replying, still above the keyboard: ' + JSON.stringify(m));
  await snap(p, 'ios-' + scheme + '-reply');

  // The whole emoji picker (a message's sheet, More reactions) is a fixed
  // bottom popup too: its search opens the keyboard, and the popup sits on
  // it, no taller than what it leaves (a smaller phone's taller keyboard
  // too), so the search and the emoji under it stay in view.
  await p.evaluate(() => window.__keyboard(0));
  await p.getByRole('button', { name: 'Message actions' }).last().evaluate((b) => b.click());
  await p.getByRole('dialog').getByRole('button', { name: 'More reactions' }).tap();
  const picker = p.getByRole('dialog', { name: 'Choose a reaction' });
  await picker.waitFor();
  await p.waitForTimeout(400); // slid up, the sheet gone
  await picker.getByLabel('Search emoji').tap();
  for (const kb of [KEYBOARD, 480]) {
    await p.evaluate((px) => window.__keyboard(px), kb);
    await frames(p);
    const e = await p.evaluate(() => {
      const sr = document.getElementById('skin').shadowRoot, search = sr.querySelector('input[aria-label="Search emoji"]');
      const box = (el) => { const r = el.getBoundingClientRect(); return [Math.round(r.top), Math.round(r.bottom)]; };
      return { popup: box(search.closest('[role="dialog"]')), search: box(search), list: box(sr.querySelector('[frimousse-viewport]')), typing: sr.activeElement === search, kb: document.documentElement.style.getPropertyValue('--an-keyboard') };
    });
    const left = PHONE.height - kb;
    assert.ok(e.typing && e.kb === kb + 'px', name + ': searching emoji with the keyboard open: ' + JSON.stringify(e));
    assert.ok(e.popup[0] >= 0 && e.popup[1] <= left, name + ': the emoji picker sits above a ' + kb + 'px keyboard: ' + JSON.stringify(e));
    assert.ok(e.search[1] <= left && e.list[1] <= left && e.list[1] - e.list[0] >= 100, name + ': its search and emoji stay in view: ' + JSON.stringify(e));
  }
  await snap(p, 'ios-' + scheme + '-emoji');
  await p.evaluate(() => window.__keyboard(0));
  await picker.getByRole('button', { name: 'Close emoji' }).tap();
  await picker.waitFor({ state: 'detached' });

  // Classic and Zoom are sized by the same host. Classic's message box sits
  // above the keyboard while typing; Zoom writes in a centred dialog of its
  // own (unchanged here), so only its box is checked.
  for (const skin of ['classic', 'zoom']) {
    await p.evaluate(() => window.__keyboard(0));
    await p.goto(origin + '/?skin=' + skin);
    await p.waitForFunction((s) => !!document.getElementById('skin').shadowRoot?.querySelector('.' + s + '-root #conv-list'), skin, { timeout: 20000 });
    if (skin === 'classic') {
      await p.locator('#conv-list .conv-item').first().click();
      const body = p.locator('#composer:not([hidden]) #body');
      await body.waitFor({ timeout: 10000 });
      await body.tap();
    }
    await p.evaluate((px) => window.__keyboard(px), KEYBOARD);
    await frames(p);
    const k = await p.evaluate(() => {
      const sr = document.getElementById('skin').shadowRoot, bottom = (e) => (e && e.getClientRects().length ? Math.round(e.getBoundingClientRect().bottom) : null);
      return { html: document.documentElement.getBoundingClientRect().height, root: bottom(sr.querySelector('.skin-root')),
        composer: bottom(sr.querySelector('#composer:not([hidden])')), typing: sr.activeElement === sr.querySelector('#body') };
    });
    assert.ok(k.html === visible && k.root <= visible, name + ' ' + skin + ': the skin fits above the keyboard: ' + JSON.stringify(k));
    if (skin === 'classic') assert.ok(k.typing && k.composer !== null && k.composer <= visible, name + ' classic: typing, the message box sits above the keyboard: ' + JSON.stringify(k));
    await snap(p, 'ios-' + scheme + '-' + skin);
    // The host's own sheets over these skins (the switcher's "Join a
    // workspace…": an invitation box and two fields) sit on the keyboard
    // too. This fixture has one workspace, so the switcher offers no Join:
    // its dialog is opened here as skinbar.mjs opens it, with that form.
    const d = await p.evaluate(() => {
      const dlg = document.getElementById('skin-bar').shadowRoot.querySelector('dialog');
      dlg.innerHTML = '<form method="dialog"><h2>Join a workspace</h2><label>Invitation<textarea rows="3"></textarea></label>'
        + '<label>What you call it<input></label><label>This device’s name there<input></label><div class="actions"><button class="btn act">Join</button><button class="btn">Cancel</button></div></form>';
      dlg.showModal();
      const r = dlg.getBoundingClientRect(), out = [Math.round(r.top), Math.round(r.bottom)];
      dlg.close();
      return out;
    });
    assert.ok(d[0] >= 0 && d[1] <= visible, name + ' ' + skin + ': the host’s sheet sits above the keyboard: ' + JSON.stringify(d));
  }
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

async function desktop(browser) {
  const name = 'desktop';
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  await ctx.addInitScript(record);
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  let m = await openChat(p, name);
  assert.equal(m.coarse, false, name + ': a fine pointer');
  assert.ok(m.focused, name + ': opening a chat puts the cursor in the field');
  assert.equal(await p.getByRole('radio', { name: 'Do it' }).count(), 0, name + ': no up-front task toggle');
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    for (const scheme of ['light', 'dark']) await android(browser, scheme);
    for (const scheme of ['light', 'dark']) await ios(browser, scheme);
    await desktop(browser);
  } finally {
    await browser.close();
  }
  console.log('keyboard viewport check PASS');
})().catch((e) => { console.error(e); process.exit(1); });
