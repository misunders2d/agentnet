const assert = require('node:assert/strict');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
  try {
    for (const fragment of ['', '#safe-fixture']) {
      const context = await browser.newContext();
      const page = await context.newPage();
      await page.route('http://bootstrap.invalid/', route => route.fulfill({ contentType: 'text/html', body: '<title>bootstrap</title>' }));
      // Keep app scripts from consuming destinations; this tests session entry.
      await page.route('**/assets/**', route => route.abort());
      await page.goto('http://bootstrap.invalid/');
      await page.goto(process.argv[2] + fragment);
      await page.waitForURL(url => !url.searchParams.has('t'), { timeout: 10000 });
      assert.equal(new URL(page.url()).hash, fragment, 'deep-link fragment preserved');
      assert.equal(await page.locator('#skin').count(), 1, 'authenticated app document loaded');
      assert.equal(await page.evaluate(async () => (await fetch('/')).status), 200, 'same-origin session established');
      const cookie = (await context.cookies()).find(cookie => cookie.name === 'agentnet_ui');
      assert(cookie && cookie.httpOnly && cookie.sameSite === 'Strict', 'session cookie remains HttpOnly/Strict');
      await page.goBack();
      assert.equal(page.url(), 'http://bootstrap.invalid/', 'handoff replaces token-bearing history entry');
      await context.close();
    }
    console.log('token handoff PASS');
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
