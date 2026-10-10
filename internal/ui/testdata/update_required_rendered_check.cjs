// Comic's "Update AgentNet to vX to continue" banner (owner policy "latest
// only", v0.8.17): shown on top, as an alert, while the overview carries
// update_required, with what the automatic update does; in the AgentNet
// app's window it offers the app's update, and a page served by a daemon
// without the app never shows an update button. At 1280 and 390 wide.
const assert = require('node:assert/strict'), fs = require('node:fs');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
(async () => {
  const browser = await chromium.launch({headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage']});
  try {
    for (const app of [false, true]) for (const width of [1280, 390]) {
      const ctx = await browser.newContext({reducedMotion: 'reduce', viewport: {width, height: 900}}), p = await ctx.newPage(), errors = [], updates = [];
      p.setDefaultTimeout(6000); p.on('pageerror', (e) => errors.push(String(e)));
      await p.route('**/api/overview*', async (route) => {
        const response = await route.fetch(), o = await response.json();
        o.app = app;
        o.update_required = {latest: 'v0.8.18', url: 'https://github.com/misunders2d/agentnet/releases/tag/v0.8.18', auto: 'Automatic update to v0.8.18 is under way.'};
        await route.fulfill({response, json: o});
      });
      await p.route('**/api/app/update', async (route) => { updates.push(route.request().postDataJSON()); await route.fulfill({json: {state: 'restarting', message: 'Restarting AgentNet with the update…'}}); });
      await p.goto(process.argv[2]);
      const banner = p.getByRole('alert').filter({hasText: 'Update AgentNet to v0.8.18 to continue'});
      await banner.waitFor();
      assert.match(await banner.innerText(), /Automatic update to v0\.8\.18 is under way/);
      const button = banner.getByRole('button', {name: 'Update now'});
      assert.equal(await button.count(), app ? 1 : 0, app ? 'the app offers its update' : 'no app: no update button');
      if (app) {
        await button.click();
        await banner.getByText('Restarting AgentNet with the update…').waitFor();
        assert.equal(updates.length, 1, 'one update request');
      }
      assert.equal(await p.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, 'no page overflow');
      if (process.env.AGENTNET_SCREENSHOTS) { fs.mkdirSync(process.env.AGENTNET_SCREENSHOTS, {recursive: true}); await p.screenshot({path: `${process.env.AGENTNET_SCREENSHOTS}/update-required-${app ? 'app' : 'daemon'}-${width}.png`}); }
      assert.deepEqual(errors, []);
      console.log(`${app ? 'app' : 'daemon'} ${width} PASS`);
      await p.unrouteAll({behavior: 'wait'}); await ctx.close();
    }
  } finally { await browser.close(); }
  console.log('Update required rendered PASS');
})().catch((e) => { console.error(e); process.exit(1); });
