const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const path = require('node:path');

async function run(browser, width, height) {
  const ctx = await browser.newContext({ viewport: { width, height } });
  const page = await ctx.newPage(), errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  const shot = async name => {
    if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `person-chat-${name}-${width}.png`) });
  };
  await page.goto(process.env.PERSON_CHAT_URL);
  const list = page.locator('section[aria-label="Chats"]');
  await list.waitFor();
  const row = list.getByRole('button', { name: /Casey/ });
  assert.equal(await row.count(), 1, 'one chat per verified person');
  await shot('list');
  await row.click();
  await page.waitForTimeout(400);
  await shot('opened');
  assert.deepEqual(errors, [], 'opening chat must not crash');
  await page.locator('[role="log"] [data-mid]', { hasText: 'Older laptop message' }).waitFor();
  await page.getByRole('button', { name: /^All topics/ }).click();
  const choices = page.getByRole('list', { name: 'Active topics', exact: true });
  assert.equal(await choices.getByRole('button').count(), 2);
  await shot('conversations');
  await choices.getByRole('button', { name: /New phone message/ }).click();
  await page.locator('[role="log"] [data-mid]', { hasText: 'New phone message' }).waitFor();
  await page.locator('[role="log"] [data-mid]', { hasText: 'Older laptop message' }).waitFor({ state: 'detached' });
  assert.equal(await page.locator('[role="log"] [data-mid]', { hasText: 'Older laptop message' }).count(), 0, 'roots are not merged');
  await page.waitForTimeout(350);
  await shot('old-conversation');
  await page.getByRole('button', { name: /^All topics/ }).click();
  await choices.getByRole('button', { name: /Empty topic/ }).click();
  await page.getByRole('textbox', { name: 'Message Casey', exact: true }).waitFor();
  await page.locator('[role="log"] [data-mid]', { hasText: 'Older laptop message' }).waitFor({ state: 'detached' });
  assert.equal(await page.locator('[role="log"] [data-mid]', { hasText: 'Older laptop message' }).count(), 0);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
  assert.equal(overflow, false, 'no page overflow');
  assert.deepEqual(errors, []);
  await ctx.close();
}
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
  try { await run(browser, 1280, 900); await run(browser, 390, 844); }
  finally { await browser.close(); }
  console.log('person chat rendered PASS');
})().catch(e => { console.error(e); process.exit(1); });
