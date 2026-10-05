// Comic on the demo fixture (comicpeople_rendered_test.go), desktop and
// phone: devices are agents only where one runs; people are people.
// Needs AGENTNET_PLAYWRIGHT; optional AGENTNET_SCREENSHOTS (outside the repo).
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const path = require('node:path');
const url = process.env.PEOPLE_URL, shots = process.env.AGENTNET_SCREENSHOTS || '';

async function run(browser, w, h) {
  const name = w + 'x' + h;
  const ctx = await browser.newContext({ viewport: { width: w, height: h } });
  const p = await ctx.newPage();
  const errors = [];
  p.on('pageerror', (e) => errors.push(String(e)));
  await p.goto(url);
  await p.waitForSelector('section[aria-label="Chats"]', { timeout: 20000 });
  const shot = async (what) => { if (shots) await p.screenshot({ path: path.join(shots, 'people-' + what + '-' + name + '.png') }); };
  const list = p.locator('section[aria-label="Chats"]');
  await shot('list');
  const text = await list.innerText();
  assert.ok(!/on Pixel/.test(text), name + ': no "on Pixel" agent row:\n' + text);
  assert.ok(/asked from your Pixel/.test(text), name + ': the phone thread is this computer\'s agent:\n' + text);
  assert.ok(/Vitalii/.test(text) && /from Phone/.test(text), name + ': Vitalii\'s phone is Vitalii:\n' + text);
  assert.ok(/on Zenbook/.test(text), name + ': Zenbook runs an agent:\n' + text);

  // The laptop↔Pixel thread: typed on the phone is yours, the answer is the agent's.
  await p.getByRole('button', { name: /asked from your Pixel/ }).first().click();
  const asked = p.locator('[role="log"] [data-mid]', { hasText: 'Did the nightly backup finish?' }).first();
  await asked.waitFor({ timeout: 10000 });
  await p.waitForTimeout(700); // the pane settles
  await shot('pixel-thread');
  assert.ok(await p.getByText('You · from Pixel').count() >= 2, name + ': "You · from Pixel" on what was typed on the phone');
  assert.equal(await p.getByRole('button', { name: /^Ask / }).count(), 0, name + ': no Ask toward the phone');
  assert.equal(await p.getByRole('textbox', { name: /^Ask / }).count(), 0, name + ': no Ask field toward the phone');
  await p.getByText('Nothing to ask here').first().waitFor({ timeout: 5000 });
  const side = await asked.evaluate((e) => {
    const bubble = [...e.querySelectorAll('*')].filter((x) => /nightly backup/.test(x.textContent) && x.getBoundingClientRect().width > 0)
      .sort((a, b) => a.getBoundingClientRect().width - b.getBoundingClientRect().width)[0] || e;
    const b = bubble.getBoundingClientRect(), l = e.closest('[role="log"]').getBoundingClientRect();
    return { right: l.right - b.right, left: b.left - l.left };
  });
  assert.ok(side.right < side.left, name + ': the phone\'s question is on the right: ' + JSON.stringify(side));

  // Vitalii's phone: a plain message only.
  if (w < 600) await p.getByRole('button', { name: /^Back to chats/ }).first().click();
  await p.waitForSelector('section[aria-label="Chats"]', { timeout: 10000 });
  await p.getByRole('button', { name: /from Phone/ }).first().click();
  await p.locator('[role="log"] [data-mid]', { hasText: 'Are we still on for the review at 4?' }).first().waitFor({ timeout: 10000 });
  await p.waitForTimeout(700);
  await shot('vitalii-thread');
  const field = p.getByRole('textbox', { name: /^Message Vitalii/ });
  await field.waitFor({ timeout: 10000 });
  assert.equal(await p.getByRole('button', { name: /^Ask / }).count(), 0, name + ': no Ask toward Vitalii\'s phone');

  // The workspace's own name, never "This server" or "AgentNet" (this page
  // is the single transport: Settings names it).
  if (w < 600) await p.getByRole('button', { name: /^Back to chats/ }).first().click();
  await (w < 600 ? p.getByRole('navigation', { name: 'Main' }).getByRole('button').last() : p.getByRole('button', { name: /^Settings/ }).first()).click();
  await p.getByText('Mellanni').first().waitFor({ timeout: 10000 });
  await p.waitForTimeout(500);
  await shot('settings');
  // Settings → Workspaces: an admin's "Name for everyone" card renames it for everyone.
  await p.getByRole('button', { name: /^Workspaces/ }).first().click();
  const everyone = p.getByRole('textbox', { name: 'Name for everyone' });
  await everyone.waitFor({ timeout: 10000 });
  assert.equal(await everyone.inputValue(), 'Mellanni', name + ': the current name');
  await everyone.fill('two\u0007bells');
  await p.getByRole('button', { name: 'Save for everyone' }).click();
  await p.getByText('Use 1–120 readable characters.').first().waitFor({ timeout: 5000 });
  await everyone.fill('Mellanni Co');
  await p.getByRole('button', { name: 'Save for everyone' }).click();
  await p.getByText('Mellanni Co').first().waitFor({ timeout: 10000 });
  await p.waitForTimeout(400);
  await shot('workspaces');
  await everyone.fill('Mellanni');
  await p.getByRole('button', { name: 'Save for everyone' }).click();
  await p.waitForTimeout(400);
  assert.deepEqual(errors, [], name + ': page errors');
  await ctx.close();
  console.log(name + ': people rows, authors, composer and workspace name ok');
}

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
  try {
    await run(browser, 1280, 900);
    await run(browser, 390, 844);
  } finally { await browser.close(); }
  console.log('comic people ui check PASS');
})().catch((e) => { console.error(e); process.exit(1); });
