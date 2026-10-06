// MEL-528 parity as Comic renders it over a real installation
// (comicparity_rendered_test.go): reminders set, moved, done and cancelled
// from a message, the list at the top of Chats with the due one first,
// "Remind me about this chat", automatic answers turned on before they
// ask and off again, and the typing preferences kept after a reload; at
// 1440x900 light and 390x844 dark (touch). No visible text names an
// address or a terminal command. Needs AGENTNET_PLAYWRIGHT; optional
// AGENTNET_SCREENSHOTS (a directory outside the repository).
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.PARITY_URL, first = process.env.PARITY_FIRST, due = process.env.PARITY_DUE;
const addresses = (process.env.PARITY_ADDRESSES || '').split(',').filter(Boolean);
const shots = process.env.AGENTNET_SCREENSHOTS || '';
const T = { timeout: 15000 };

// localValue: a datetime-local value in this machine's time (the browser's too).
const localValue = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60e3).toISOString().slice(0, 16);

async function noAddresses(p, name, what) {
  const text = await p.evaluate(() => {
    const sr = document.getElementById('skin').shadowRoot;
    // What is shown, sheets and menus included (they render inside the root).
    return sr.textContent || '';
  });
  for (const a of addresses) assert.ok(!text.includes(a), name + ' ' + what + ': shows the address ' + a);
  assert.ok(!/\bagentnet [a-z]/.test(text), name + ' ' + what + ': tells the person to run a command');
}

async function desktop(browser) {
  const name = '1440x900-light';
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  const snap = async (what) => { if (shots) { await p.waitForTimeout(400); await p.screenshot({ path: shots + '/parity-' + name + '-' + what + '.png' }); } }; // after sheets settle
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', T);

  // The reminder already due leads the list, with its sender by name.
  const list = p.locator('section[aria-labelledby="chats-reminders"]');
  await list.waitFor(T);
  assert.match(await list.textContent(), /1 due/, name + ': the list says one is due');
  const dueRow = list.getByRole('button', { name: /^The Denver pallets/ });
  assert.match(await dueRow.textContent(), /Due today .*· From Laptop/, name + ': due row says when, then who');
  await snap('list');
  await noAddresses(p, name, 'chat list');

  // It opens its message, which shows the due line.
  await dueRow.click();
  const dueMsg = p.locator('[data-mid="' + due + '"]');
  await dueMsg.getByText(/^Reminder due since /).waitFor(T);
  await dueMsg.getByRole('button', { name: 'Later…' }).waitFor(T);

  // Remind me… from the first message's menu.
  const msg = p.locator('[data-mid="' + first + '"]');
  await msg.locator('[role="group"]').hover();
  await msg.getByRole('button', { name: 'More actions' }).click();
  await p.getByRole('menuitem', { name: 'Remind me…' }).click();
  const sheet = p.getByRole('dialog', { name: 'Remind me later' });
  await sheet.waitFor(T);
  assert.match(await sheet.textContent(), /Times are this computer’s/, name + ': the sheet says whose time');
  assert.match(await sheet.textContent(), /even with this window closed/, name + ': the sheet says it fires with the window closed');
  await snap('remind-sheet');
  await noAddresses(p, name, 'remind sheet');
  const [morning, morningSub] = await sheet.locator('label', { hasText: 'Tomorrow at' }).locator('span.block').allTextContents();
  assert.equal(morning.replace('Tomorrow at ', ''), morningSub.replace('tomorrow ', ''), name + ': the morning choice writes its time as its subtitle does');
  await sheet.getByText('In 2 hours').click();
  await sheet.getByRole('button', { name: 'Remind me' }).click();
  await msg.getByText(/^Reminder (today|tomorrow) /).waitFor(T);
  await list.getByRole('button', { name: /^Please check the Savannah order/ }).waitFor(T);
  await snap('reminder-set');

  // Change… moves it to a time chosen.
  await msg.getByRole('button', { name: 'Change…' }).click();
  const move = p.getByRole('dialog', { name: 'Move the reminder' });
  await move.waitFor(T);
  const later = new Date(Date.now() + 26 * 3600e3);
  await move.locator('input[type="datetime-local"]').fill(localValue(later));
  await move.getByRole('button', { name: 'Move it' }).click();
  await move.waitFor({ state: 'hidden', ...T });
  await msg.getByText(/^Reminder (tomorrow|\w{3}, )/).waitFor(T);

  // The header menu reminds about the whole chat (its newest received message).
  await p.getByRole('button', { name: 'More', exact: true }).click();
  await p.getByRole('menuitem', { name: /Change reminder…|Remind me about this chat…/ }).waitFor(T);
  await p.keyboard.press('Escape');

  // Done and Cancel end them; the list goes with the last one.
  await msg.getByRole('button', { name: 'Done', exact: true }).click();
  await msg.getByText(/^Reminder /).waitFor({ state: 'detached', ...T });
  await dueMsg.getByRole('button', { name: 'Cancel', exact: true }).click();
  await dueMsg.getByText(/^Reminder /).waitFor({ state: 'detached', ...T });
  await list.waitFor({ state: 'detached', ...T });

  // Automatic answers, turned on before they ask, and off again.
  await p.getByRole('button', { name: 'You: profile and devices' }).click();
  await p.getByRole('button', { name: /^Permissions/ }).click();
  await p.getByRole('button', { name: 'Turn on automatic answers for Laptop' }).click();
  const ask = p.getByRole('alertdialog', { name: 'Answer Laptop’s questions automatically?' });
  await ask.waitFor(T);
  await noAddresses(p, name, 'approve ahead');
  await snap('approve');
  await ask.getByRole('button', { name: 'Answer automatically' }).click();
  await p.getByRole('button', { name: 'Turn off automatic answers for Laptop' }).click();
  const stop = p.getByRole('alertdialog', { name: 'Stop answering Laptop’s questions automatically?' });
  await stop.getByRole('button', { name: 'Stop automatic answers' }).click();
  await p.getByRole('button', { name: 'Turn on automatic answers for Laptop' }).waitFor(T);

  // Typing preferences are kept on this device.
  const ready = (want) => p.waitForFunction((want) => {
    const s = document.getElementById('skin').shadowRoot.querySelector('[role="switch"][aria-label="Share when I’m typing"]');
    return s && !s.hasAttribute('data-disabled') && !s.hasAttribute('aria-disabled') && (want === null || s.getAttribute('aria-checked') === want);
  }, want, T);
  await p.getByRole('button', { name: /^Notifications/ }).click();
  await ready(null);
  const share = p.getByRole('switch', { name: 'Share when I’m typing' });
  const was = await share.getAttribute('aria-checked');
  await share.click();
  await p.getByText('Saved on this device.').waitFor(T);
  await snap('typing');
  await p.reload();
  await p.waitForSelector('section[aria-label="Chats"]', T);
  await p.getByRole('button', { name: 'You: profile and devices' }).click();
  await p.getByRole('button', { name: /^Notifications/ }).click();
  await ready(was === 'true' ? 'false' : 'true'); // kept after a reload
  await p.getByRole('switch', { name: 'Share when I’m typing' }).click();
  await p.getByText('Saved on this device.').waitFor(T);
  await ready(was);

  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

async function phone(browser) {
  const name = '390x844-dark';
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, colorScheme: 'dark', isMobile: true, hasTouch: true });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  const snap = async (what) => { if (shots) { await p.waitForTimeout(400); await p.screenshot({ path: shots + '/parity-' + name + '-' + what + '.png' }); } }; // after sheets settle
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', T);
  // P2 names the peer device; its person-link status is a separate subtitle.
  await p.getByRole('list', { name: 'Chats', exact: true }).getByRole('button', { name: /^Laptop\b/ }).click();
  const msg = p.locator('[data-mid="' + first + '"]');
  await msg.waitFor(T);
  // The message's actions (a long-press opens the same sheet).
  await msg.getByRole('button', { name: 'Message actions' }).focus();
  await p.keyboard.press('Enter');
  await p.getByRole('button', { name: 'Remind me…' }).click();
  const sheet = p.getByRole('dialog', { name: 'Remind me later' });
  await sheet.waitFor(T);
  await snap('remind-sheet');
  const fits = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.ok(fits <= 0, name + ': nothing scrolls sideways');
  await sheet.getByRole('button', { name: 'Remind me' }).click();
  await msg.getByText(/^Reminder today |^Reminder tomorrow /).waitFor(T);
  await snap('reminder-set');
  await p.getByRole('button', { name: /^Back to chats/ }).click();
  const list = p.locator('section[aria-labelledby="chats-reminders"]');
  await list.waitFor(T);
  await snap('list');
  await noAddresses(p, name, 'chat list');
  await list.getByRole('button', { name: /^Reminder done:/ }).click();
  await list.waitFor({ state: 'detached', ...T });
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    await desktop(browser);
    await phone(browser);
    console.log('comic parity check PASS');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
