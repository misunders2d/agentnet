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
  // An unbound historical agent thread has no list row since 9133893b (model.ts,
  // currentAgentTarget), so the laptop-Pixel and Zenbook threads are no longer reachable from here.
  assert.ok(!/asked from your Pixel/.test(text), name + ': a retired agent thread has no row:\n' + text);
  assert.ok(/Vitalii/.test(text) && /from Phone/.test(text), name + ': Vitalii\'s phone is Vitalii:\n' + text);

  // Vitalii's phone: a plain message only.
  await p.getByRole('button', { name: /from Phone/ }).first().click();
  await p.locator('[role="log"] [data-mid]', { hasText: 'Are we still on for the review at 4?' }).first().waitFor({ timeout: 10000 });
  await p.waitForTimeout(700);
  await shot('vitalii-thread');
  // An old device conversation takes no new turn since 4825ff89 (MEL-713).
  const field = p.getByRole('textbox', { name: 'Read only: write in a chat' });
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
