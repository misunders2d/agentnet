// A topic with a question waiting for the person says so: as a chip where the
// bar has room, on the All topics chip where it has not (TestTopicNeedsYouRendered).
const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || undefined });
  try {
    for (const [name, width, height] of [['desktop', 1440, 900], ['phone', 390, 844]]) {
      const context = await browser.newContext({ viewport: { width, height } });
      const p = await context.newPage();
      const errors = [];
      p.on('pageerror', e => errors.push(e.message));
      await p.goto(process.env.TOPICS_URL);
      await p.getByRole('button', { name: /Dock helper/ }).first().click({ timeout: 20000 });
      const bar = p.getByRole('navigation', { name: /topics/i }).first();
      await bar.waitFor({ timeout: 15000 });
      if (width >= 1000) {
        await bar.getByRole('button', { name: /^May I move the Savannah order.*Needs you/ }).waitFor({ timeout: 15000 });
      } else {
        // One topic chip keeps room for its name beside All; the questions not in
        // the bar mark the All topics chip, and its label says how many.
        const all = bar.getByRole('button', { name: /^All topics/ });
        await all.waitFor({ timeout: 15000 });
        const label = await all.getAttribute('aria-label');
        const openNeeds = /Needs you/.test(await bar.locator('[aria-current="true"]').getAttribute('aria-label') || '');
        assert.match(label, new RegExp('\\b' + (4 - openNeeds) + ' need you\\b'), name + ': the All topics chip says what needs you: ' + label);
      }
      assert.deepEqual(errors, [], name + ': page errors');
      await context.close();
      console.log('ok', name);
    }
    console.log('topic needs you check PASS');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
