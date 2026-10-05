// The group parts of MEL-528 as Comic renders them (comicparity_rendered_test.go,
// TestComicGroupParityRendered): Alice, the group's admin, renames it from
// the header menu, makes Bob an admin from "In this chat" and takes it back,
// is refused leaving as its last admin (in words, the sheet stays), and
// brings in Bob's agent sharing "since a date" with Bob ticked to give it
// tasks without asking: tasks_from is exactly Bob's member keys. Then the
// phone's room sheet offers the same. Needs AGENTNET_PLAYWRIGHT; optional
// AGENTNET_SCREENSHOTS.
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.PARITY_URL;
const bobKeys = (process.env.PARITY_BOB_KEYS || '').split(',').filter(Boolean).sort();
const addresses = (process.env.PARITY_ADDRESSES || '').split(',').filter(Boolean);
const shots = process.env.AGENTNET_SCREENSHOTS || '';
const T = { timeout: 20000 };
const localValue = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60e3).toISOString().slice(0, 16);

async function noAddresses(p, name, what) {
  const text = await p.evaluate(() => document.getElementById('skin').shadowRoot.textContent || '');
  for (const a of addresses) assert.ok(!text.includes(a), name + ' ' + what + ': shows the address ' + a);
  assert.ok(!/\bagentnet [a-z]/.test(text), name + ' ' + what + ': tells the person to run a command');
}

async function desktop(browser) {
  const name = '1440x900-light';
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  const snap = async (what) => { if (shots) { await p.waitForTimeout(400); await p.screenshot({ path: shots + '/group-' + name + '-' + what + '.png' }); } };
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', T);
  await p.getByRole('button', { name: /Dock crew/ }).first().click();
  await p.locator('[role="log"]').getByText('Savannah order ships Friday').waitFor(T);

  // Rename from the header menu.
  await p.getByRole('button', { name: 'More', exact: true }).click();
  await p.getByRole('menuitem', { name: 'Leave group…' }).waitFor(T);
  await p.getByRole('menuitem', { name: 'Rename group…' }).click();
  const rename = p.getByRole('dialog', { name: 'Rename the group' });
  await rename.getByRole('textbox').fill('Dock crew east');
  await snap('rename');
  await rename.getByRole('button', { name: 'Rename' }).click();
  await p.locator('header').getByText('Dock crew east', { exact: true }).waitFor(T);

  // Make Bob an admin, then take it back, from "In this chat".
  await p.getByRole('button', { name: /Who’s in this chat/ }).click();
  const panel = p.getByRole('complementary', { name: 'In this chat' });
  await panel.waitFor(T);
  await panel.getByRole('button', { name: 'Rename group…' }).waitFor(T);
  await panel.getByRole('button', { name: 'Leave group…' }).waitFor(T);
  const bobRow = panel.locator('li', { hasText: 'Bob' });
  await bobRow.getByRole('button', { name: /^Change Bob/ }).click();
  await p.getByRole('menuitem', { name: 'Make admin…' }).click();
  const promote = p.getByRole('dialog', { name: 'Make Bob an admin?' });
  await snap('promote');
  await noAddresses(p, name, 'make admin');
  await promote.getByRole('button', { name: 'Make admin' }).click();
  await bobRow.getByText('Admin', { exact: true }).waitFor(T);
  await bobRow.getByRole('button', { name: /^Change Bob/ }).click();
  await p.getByRole('menuitem', { name: 'Remove admin role…' }).click();
  await p.getByRole('dialog', { name: 'Remove Bob’s admin role?' }).getByRole('button', { name: 'Remove admin role' }).click();
  await bobRow.getByText('Admin', { exact: true }).waitFor({ state: 'detached', ...T });

  // Bring in Bob's agent: since a date, and Bob may give it tasks without asking.
  await p.getByRole('button', { name: 'Bring in', exact: true }).click();
  const sheet = p.getByRole('dialog', { name: 'Bring someone in' });
  await sheet.getByRole('radio', { name: /Bob’s agent/ }).check({ force: true });
  const since = sheet.locator('input[type="datetime-local"]');
  await since.fill(localValue(new Date(Date.now() - 3600e3)));
  await sheet.getByText(/^3 messages$/).waitFor(T);
  const tasks = sheet.getByRole('group', { name: 'Who can give it tasks without asking' });
  const you = tasks.getByRole('checkbox', { name: /You/ }), bob = tasks.getByRole('checkbox', { name: /Bob/ });
  assert.equal(await you.isChecked(), false, name + ': nobody is ticked for someone else’s agent');
  assert.equal(await bob.isChecked(), false, name + ': nobody is ticked for someone else’s agent');
  await tasks.locator('label', { hasText: 'Bob' }).click();
  assert.equal(await bob.isChecked(), true, name + ': Bob is ticked');
  await snap('invite');
  await noAddresses(p, name, 'invite');
  const sent = p.waitForRequest((r) => r.url().includes('/api/dm/agent/invite'), T);
  await sheet.getByRole('button', { name: /^Bring Bob’s agent in/ }).click();
  const body = JSON.parse((await sent).postData() || '{}');
  assert.deepEqual([...(body.tasks_from || [])].sort(), bobKeys, name + ': tasks_from is Bob’s member keys');
  assert.equal((body.share || []).length, 3, name + ': the messages since the date are shared');

  // The last admin can't leave: said in words, the sheet stays.
  await p.getByRole('button', { name: 'More', exact: true }).click();
  await p.getByRole('menuitem', { name: 'Leave group…' }).click();
  const leave = p.getByRole('dialog', { name: /^Leave “Dock crew east”\?/ });
  await leave.getByRole('button', { name: 'Leave group' }).click();
  await leave.getByRole('alert').waitFor(T);
  await snap('leave-refused');
  await leave.getByRole('button', { name: 'Cancel' }).click();
  await leave.waitFor({ state: 'hidden', ...T });

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
  const snap = async (what) => { if (shots) { await p.waitForTimeout(400); await p.screenshot({ path: shots + '/group-' + name + '-' + what + '.png' }); } };
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', T);
  await p.getByRole('button', { name: /Dock crew east/ }).first().click();
  await p.getByRole('button', { name: /Who’s in this chat/ }).click();
  const room = p.getByRole('dialog', { name: 'In this chat' });
  await room.getByRole('button', { name: /^Change Bob/ }).waitFor(T);
  await room.getByRole('button', { name: 'Rename group…' }).waitFor(T);
  await room.getByRole('button', { name: 'Leave group…' }).waitFor(T);
  await snap('room');
  const fits = await p.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  assert.ok(fits <= 0, name + ': nothing scrolls sideways');
  await noAddresses(p, name, 'room sheet');
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log('ok', name);
}

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    await desktop(browser);
    await phone(browser);
    console.log('comic group parity check PASS');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
