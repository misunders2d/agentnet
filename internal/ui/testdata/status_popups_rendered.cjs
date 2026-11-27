const assert = require('node:assert/strict');
const path = require('node:path');

// Run against the same production host and Comic bundle as notification landing.
async function checkStatusPopups({browser, origin, width, count, fixture, evidence, conv, target, missing, editable, hash, visibleTarget, push, release}) {
  const phone = width < 1024;
  const context = await browser.newContext({viewport: {width, height: phone ? 844 : 900}, hasTouch: phone, reducedMotion: 'reduce'});
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  const errors = [], timings = {};
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => {
    if (message.type() === 'warning' || (message.type() === 'error' && !message.text().includes('status of 409'))) errors.push(message.text());
  });
  await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
  const field = page.getByRole('textbox', {name: 'Message Casey', exact: true});
  const status = page.locator('[data-status-popup]');
  const working = status.filter({hasText: 'Working on it…'});
  const landing = status.filter({has: page.getByRole('button', {name: 'Dismiss', exact: true})});
  const editError = status.filter({hasText: 'Invented edit refusal'});
  const edited = status.filter({hasText: 'Edited'});
  const row = page.locator(`[data-mid="${editable}"]`);
  const editAgain = row.getByRole('button', {name: 'Edit again', exact: true});
  const click = locator => phone ? locator.tap() : locator.click();
  const frame = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve(performance.now())))));
  const feedback = async (name, act, observed) => {
    const start = await page.evaluate(() => performance.now());
    await act();
    await observed();
    const ms = await frame() - start;
    timings[name] = Math.ceil(ms);
    assert(ms < 1100, `${name} took ${ms.toFixed(1)} ms at ${width}px with ${count} history rows`);
    return start;
  };
  const dismiss = (popup, name, label) => feedback(name,
    () => click(popup.getByRole('button', {name: label, exact: true})),
    () => popup.waitFor({state: 'detached'}));
  const typeWith = async (popup, name, char) => {
    await popup.waitFor();
    const geometry = await popup.evaluate(el => {
      const root = el.getRootNode();
      const composer = root.querySelector('[data-part="composer"]');
      const topics = root.querySelector('[data-part="topic-bar"]');
      const rect = el.getBoundingClientRect();
      return {aboveComposer: rect.bottom <= composer.getBoundingClientRect().top,
        belowTopics: rect.top >= topics.getBoundingClientRect().bottom, pointer: getComputedStyle(el).pointerEvents};
    });
    assert.deepEqual(geometry, {aboveComposer: true, belowTopics: true, pointer: 'none'}, `${name} must leave composer and topic controls clear, with click-through text`);
    const before = await field.inputValue();
    await feedback(name + 'Focus', () => click(field), () => page.waitForFunction(() =>
      document.querySelector('#skin').shadowRoot.activeElement?.matches('[data-part="composer"] textarea')));
    await feedback(name + 'Type', () => page.keyboard.type(char), () => page.waitForFunction(value =>
      document.querySelector('#skin').shadowRoot.activeElement?.value === value, before + char));
    assert(await popup.isVisible(), `${name} status remains visible while typing`);
  };
  const openMessageActions = () => row.locator('[data-message-bubble]').click({button: 'right'});
  const editMenu = async () => {
    await openMessageActions();
    await click(page.getByRole('dialog').getByRole('button', {name: 'Edit', exact: true}));
  };
  const editor = page.getByRole('textbox', {name: 'New text', exact: true});
  const waitForEdit = async () => {
    await waitForRequest(() => fixture.edit, 'edit');
    const attempt = fixture.edit;
    fixture.edit = null;
    return attempt;
  };
  const openMissing = () => page.evaluate(value => { location.hash = value; }, hash(missing));
  const waitForHeldRead = () => waitForRequest(() => fixture.held.length, 'held DM read');
  const finishHeldReads = async () => {
    const response = page.waitForResponse(r => r.url().includes('/api/dm?id=') && r.ok());
    release();
    await (await response).finished();
  };

  try {
    await page.goto(origin + '/#' + hash(target));
    await visibleTarget(page, target);
    await field.waitFor();
    assert.match(await page.title(), /AgentNet/);
    await editMenu();
    await editor.fill('Keep this exact edit');
    const save = page.getByRole('button', {name: 'Save', exact: true});
    const saving = row.getByText('Saving edit…', {exact: true});
    // Measure the delayed notice in the renderer from the actual Save click.
    // Playwright can take time to dispatch the click or resume after a visible frame.
    await save.evaluate(button => {
      const root = button.getRootNode();
      window.firstWorkingMs = null;
      button.addEventListener('click', event => {
        const checkFrame = () => {
          const elapsed = performance.now() - event.timeStamp;
          if (root.querySelector('[data-working]')?.checkVisibility()) {
            requestAnimationFrame(() => { window.firstWorkingMs = performance.now() - event.timeStamp; });
          } else if (elapsed >= 1100) {
            window.firstWorkingMs = elapsed;
          } else {
            requestAnimationFrame(checkFrame);
          }
        };
        requestAnimationFrame(checkFrame);
      }, {capture: true, once: true});
    });
    await feedback('editPending', () => click(save), () => saving.waitFor());
    const attempt = await waitForEdit();
    assert.equal(attempt.body.text, 'Keep this exact edit');
    assert.equal(attempt.body.id, editable);
    const workingMs = await (await page.waitForFunction(() => window.firstWorkingMs)).jsonValue();
    assert(workingMs < 1100, `Working missed the 1100 ms deadline at ${width}px with ${count} history rows (${workingMs.toFixed(1)} ms)`);
    timings.workingShown = Math.ceil(workingMs);

    fixture.holdDM = true;
    const syncText = 'Linked-device edit arrived during the pending write';
    const incoming = fixture.views[conv].messages.find(message => message.id === target);
    Object.assign(incoming, {text: syncText, edited: true, revision: 2});
    push();
    await waitForHeldRead();
    await typeWith(working, 'working', 'W');
    if (evidence && count === 100) await page.screenshot({path: path.join(evidence, `status-working-${width}.png`)});
    await dismiss(working, 'workingDismiss', 'Dismiss working status');
    await saving.waitFor();
    await finishHeldReads();
    await page.locator(`[data-mid="${target}"]`).getByText(syncText).waitFor();
    await saving.waitFor();
    assert.equal(await working.count(), 0, 'sync does not recreate a dismissed Working notice');

    attempt.resolve(false);
    await editError.waitFor();
    assert.equal(await editError.getAttribute('role'), 'alert');
    await editAgain.waitFor();
    await typeWith(editError, 'error', 'E');
    const mention = page.getByRole('button', {name: 'Mention someone', exact: true});
    await feedback('errorComposerButton', () => click(mention), () => page.getByRole('listbox').waitFor());
    await page.keyboard.press('Escape');
    await page.getByRole('listbox').waitFor({state: 'hidden'});
    assert(await editError.isVisible(), 'Escape in the composer preserves a readable error');
    if (evidence && count === 100) await page.screenshot({path: path.join(evidence, `status-error-${width}.png`)});
    await dismiss(editError, 'errorDismiss', 'Dismiss status');
    await row.getByText('Original editable message').waitFor();
    await click(editAgain);
    assert.equal(await editor.inputValue(), 'Keep this exact edit', 'dismissal keeps the actionable failed edit');
    const retryStart = await feedback('editRetryPending', () => click(save), () => saving.waitFor());
    const retry = await waitForEdit();
    assert.deepEqual(retry.body, attempt.body);
    await working.waitFor();
    while (await page.evaluate(() => performance.now()) - retryStart < 1150) await frame();
    // Keep the next success toast alive for the Escape ordering checks. Those
    // checks test dismissal priority, independently of the real expiry timer.
    await page.evaluate(() => {
      const schedule = window.setTimeout;
      window.statusPriority = {noticeAt: null, escape: null};
      window.setTimeout = (callback, delay, ...args) => {
        if (delay !== 3500) return schedule.call(window, callback, delay, ...args);
        window.setTimeout = schedule;
        window.statusPriority.noticeAt = performance.now();
        return schedule.call(window, () => {}, delay);
      };
    });
    retry.resolve(true);
    await edited.waitFor();
    assert(await page.evaluate(() => window.statusPriority.noticeAt !== null), 'success toast expiry is held for the priority check');
    await row.getByText('Keep this exact edit').waitFor();
    timings.editConfirmed = Math.ceil(await frame() - retryStart);
    assert(timings.editConfirmed >= 1150, 'edit confirmation was intentionally held past the local feedback bound');

    await typeWith(edited, 'ok', 'O');
    await openMessageActions();
    const sheet = page.getByRole('dialog');
    await sheet.waitFor();
    await page.keyboard.press('Escape');
    await sheet.waitFor({state: 'hidden'});
    assert(await edited.isVisible(), 'the foreground sheet gets Escape before a status');
    await openMessageActions();
    await click(sheet.getByRole('button', {name: 'Select', exact: true}));
    const stopSelecting = page.getByRole('button', {name: 'Stop selecting'});
    await stopSelecting.waitFor();
    await sheet.waitFor({state: 'hidden'});
    assert(await edited.isVisible(), 'both selection and success toast are present before Escape');
    await page.evaluate(() => {
      window.addEventListener('keydown', () => {
        const root = document.querySelector('#skin').shadowRoot;
        const state = window.statusPriority;
        state.escape = {noticeAgeMs: Math.round(performance.now() - state.noticeAt),
          selection: !!root.querySelector('[data-message-selection]')?.getClientRects().length};
      }, {capture: true, once: true});
    });
    await page.keyboard.press('Escape');
    await stopSelecting.waitFor({state: 'detached'});
    await field.waitFor();
    const escape = await page.evaluate(() => window.statusPriority.escape);
    assert(escape?.selection, 'selection is still present when Escape starts');
    assert(await edited.isVisible(), 'message selection gets Escape before a status: ' + JSON.stringify(escape));
    await dismiss(edited, 'okDismiss', 'Dismiss status');

    fixture.holdDM = true;
    await openMissing();
    await waitForHeldRead();
    await typeWith(landing, 'landingBusy', 'B');
    assert.equal(await landing.getAttribute('aria-busy'), 'true');
    await dismiss(landing, 'landingBusyDismiss', 'Dismiss');
    await finishHeldReads();
    assert.equal(await landing.count(), 0, 'a late landing response does not reopen the dismissed status');

    await page.evaluate(value => { location.hash = value; }, 'conv=' + conv + '&workspace=default');
    await field.waitFor();
    await openMissing();
    const tryAgain = landing.getByRole('button', {name: 'Try again', exact: true});
    await tryAgain.waitFor();
    await typeWith(landing, 'landingError', 'L');
    fixture.holdDM = true;
    await feedback('landingRetryPending', () => click(tryAgain), () => landing.getByText('Opening the notified message…').waitFor());
    await waitForHeldRead();
    await finishHeldReads();
    await tryAgain.waitFor();
    await dismiss(landing, 'landingErrorDismiss', 'Dismiss');

    // A newer landing alert wins Escape while an older Working notice remains.
    await editMenu();
    await editor.fill('Second edit');
    await click(save);
    const second = await waitForEdit();
    await working.waitFor();
    await page.evaluate(value => { location.hash = value; }, 'conv=' + conv + '&workspace=default');
    await openMissing();
    await tryAgain.waitFor();
    await field.blur();
    await feedback('landingEscape', () => page.keyboard.press('Escape'), () => landing.waitFor({state: 'detached'}));
    await working.waitFor();
    await feedback('workingEscape', () => page.keyboard.press('Escape'), () => working.waitFor({state: 'detached'}));
    second.resolve(true);
    await edited.waitFor();
    await feedback('toastEscape', () => page.keyboard.press('Escape'), () => edited.waitFor({state: 'detached'}));
    assert.equal(await field.inputValue(), 'WEOBL', 'navigation, retry and dismissal keep the same composer draft');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({width, history: fixture.views[conv].messages.length, phoneTouch: phone, timings, errors, result: 'PASS'}));
  } finally {
    release();
    await context.close();
  }
}

async function waitForRequest(read, description) {
  for (let tries = 0; tries < 250; tries++) {
    if (read()) return;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.fail('Timed out waiting for fixture ' + description);
}

module.exports = {checkStatusPopups};
