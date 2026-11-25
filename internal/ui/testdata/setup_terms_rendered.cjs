// Rendered first-run page: the terms of use checkbox gates Google sign-in,
// Join and Add this computer; refusal and acceptance give feedback at once.
const assert = require('node:assert/strict'), path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
const [setup, token] = process.argv.slice(2);
const shots = process.env.AGENTNET_SCREENSHOTS;
const problem = 'Agree to the Terms of use to continue.';
const timings = [];
async function timed(label, act, done) {
  const t0 = Date.now(); await act(); await done(); const ms = Date.now() - t0;
  timings.push(label + ' ' + ms + 'ms'); assert.ok(ms < 1100, label + ' took ' + ms + 'ms');
}
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', headless: true });
  try {
    for (const width of [1280, 390]) {
      const context = await browser.newContext({ viewport: { width, height: 860 }, colorScheme: width === 390 ? 'dark' : 'light' });
      await context.addCookies([{ name: 'agentnet_ui', value: token, url: setup }]);
      const page = await context.newPage();
      const errors = []; page.on('pageerror', e => errors.push(String(e)));
      const capture = async name => { if (shots) await page.screenshot({ path: path.join(shots, `setup-terms-${name}-${width}.png`), fullPage: true }); };
      const terms = () => page.getByRole('checkbox', { name: /I agree to the Terms of use/ });

      // Welcome: Google sign-in waits for the checkbox.
      await page.goto(setup + '/?t=' + token);
      await terms().waitFor();
      assert.equal(await terms().isChecked(), false);
      assert.equal(await page.getByRole('link', { name: 'Terms of use' }).getAttribute('href'), 'https://github.com/misunders2d/agentnet/blob/main/TERMS.md');
      assert.equal(await page.getByRole('link', { name: 'Privacy policy' }).getAttribute('href'), 'https://github.com/misunders2d/agentnet/blob/main/PRIVACY.md');
      await page.getByLabel('Workspace address').fill('https://workspace.example');
      await timed('google refused', () => page.getByRole('button', { name: 'Sign in with Google', exact: true }).click(), () => page.getByText(problem, { exact: true }).waitFor());
      assert.equal(await terms().evaluate(e => e === document.activeElement), true, 'focus moves to the checkbox');
      await capture('welcome-refused');
      await timed('accept clears', () => terms().check(), () => page.getByText(problem, { exact: true }).waitFor({ state: 'detached' }));
      await timed('google after accept', () => page.getByRole('button', { name: 'Sign in with Google', exact: true }).click(), () => page.getByText('Fixture sign-in stopped here.', { exact: true }).waitFor());

      // Invitation card: a fresh page starts unaccepted.
      await page.goto(setup + '/?t=' + token + '#' + encodeURIComponent('agentnet-invite-v1:fixture'));
      await page.getByLabel('Your name').fill('Bea');
      assert.equal(await terms().isChecked(), false);
      await timed('join refused', () => page.getByRole('button', { name: 'Join', exact: true }).click(), () => page.getByText(problem, { exact: true }).waitFor());
      await capture('invite-refused');
      await terms().check();
      await timed('join after accept', () => page.getByRole('button', { name: 'Join', exact: true }).click(), () => page.getByText('Fixture join stopped here.', { exact: true }).waitFor());
      assert.equal(await page.getByRole('button', { name: 'Join', exact: true }).isEnabled(), true);

      // Device link card.
      await page.goto(setup + '/?t=' + token + '#' + encodeURIComponent('agentnet-link-v2:fixture'));
      await page.getByRole('button', { name: 'Add this computer', exact: true }).waitFor();
      await timed('link refused', () => page.getByRole('button', { name: 'Add this computer', exact: true }).click(), () => page.getByText(problem, { exact: true }).waitFor());
      await terms().check();
      await capture('link-accepted');
      await timed('link after accept', () => page.getByRole('button', { name: 'Add this computer', exact: true }).click(), () => page.getByText('Fixture join stopped here.', { exact: true }).waitFor());
      assert.deepEqual(errors, []);
      await context.close();
    }
    console.log('Setup terms PASS ' + timings.join(', '));
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exit(1); });
