// A changed identity and the held-back list as Comic renders them over the
// demo installation (comicparity_rendered_test.go, TestComicTrustRendered).
// PARITY_SIZE: 1440x900-light or 390x844-dark. Needs AGENTNET_PLAYWRIGHT;
// optional AGENTNET_SCREENSHOTS.
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assert = require('node:assert/strict');
const url = process.env.PARITY_URL, name = process.env.PARITY_SIZE;
const shots = process.env.AGENTNET_SCREENSHOTS || '';
const T = { timeout: 15000 };
const address = /\b[a-z0-9._-]+\/[a-z0-9._-]+\b/;

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    const [w, h] = name.split('-')[0].split('x').map(Number), scheme = name.split('-')[1];
    const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme, ...(w < 1024 ? { isMobile: true, hasTouch: true } : {}) });
    const p = await ctx.newPage();
    const errors = [];
    p.on('pageerror', (e) => errors.push(String(e)));
    const snap = async (what) => { if (shots) { await p.waitForTimeout(400); await p.screenshot({ path: shots + '/trust-' + name + '-' + what + '.png' }); } };
    await p.goto(url);
    await p.waitForSelector('section[aria-label="Chats"]', T);

    // The chat row says so; the paused composer offers the check.
    const row = p.getByRole('button', { name: /Identity changed/ });
    await row.first().click();
    await p.getByText('Sending is paused').waitFor(T);
    await p.getByRole('button', { name: 'Check and trust…' }).click();
    const sheet = p.getByRole('dialog', { name: /new identity$/ });
    await sheet.waitFor(T);
    const text = await sheet.textContent();
    assert.ok(!address.test(text), name + ': the trust sheet shows an address: ' + text);
    assert.ok(!/agentnet [a-z]/.test(text), name + ': the trust sheet names a command');
    assert.match(text, /Settings → Your devices/, name + ': it says where the owner finds their code');
    const trust = sheet.getByRole('button', { name: 'Trust the new identity' });
    assert.equal(await trust.isDisabled(), true, name + ': Trust waits for the codes to be compared');
    await snap('sheet');
    await sheet.locator('label', { hasText: 'The new code matches what they told me' }).click();
    await trust.click();
    await sheet.waitFor({ state: 'hidden', ...T });
    await p.getByText('Sending is paused').waitFor({ state: 'detached', ...T });

    // OKs lists what was held back, by name, in words.
    if (w < 1024) await p.getByRole('button', { name: /^Back to chats/ }).click();
    await p.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: /^OKs/ }).click();
    const held = p.locator('section[aria-labelledby="oks-heldback"]');
    await held.waitFor(T);
    const words = await held.textContent();
    assert.match(words, /couldn’t be verified/, name + ': the reason is in words');
    // The demo's unverified message only claims a sender: it isn't drawn as that person.
    const unverified = held.locator('li', { hasText: 'A message that couldn’t be verified' });
    assert.match(await unverified.textContent(), /says it’s from /, name + ': an unverified message says who it claims to be from');
    assert.equal(await unverified.locator('[data-size]').count(), 0, name + ': an unverified message has no person’s avatar');
    assert.ok(!address.test(words), name + ': the held-back list shows an address: ' + words);
    await held.scrollIntoViewIfNeeded();
    await snap('held');
    assert.deepEqual(errors, [], name + ': page errors');
    await ctx.close();
    console.log('ok', name);
    console.log('comic trust check PASS');
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
