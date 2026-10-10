// Comic, rendered: OKs never present a stale or running host word as a
// decision, a suspended device's copy says so, and a message a member's
// device was not sent at all never reads as delivered
// (peertolerance_rendered_test.go).
const assert = require('node:assert/strict');
const {chromium} = require(process.env.AGENTNET_PLAYWRIGHT);
(async () => {
  const browser = await chromium.launch({headless:true,executablePath:process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium',args:['--no-sandbox','--disable-dev-shm-usage']});
  try {
    const ctx = await browser.newContext({viewport:{width:1280,height:900}});
    const p = await ctx.newPage(), errors = [];
    p.on('console',m=>{if(m.type()==='error' && !/^Failed to load resource: the server responded with a status of 404/.test(m.text())) errors.push(m.text());});
    p.on('pageerror', e=>errors.push(String(e)));
    await p.goto(process.argv[2]);
    await p.goto(new URL(process.argv[2]).origin+'/?skin=comic');
    await p.getByRole('navigation',{name:'Main'}).getByRole('button',{name:/^OKs/}).click();
    const card = id => p.locator('[data-card$="'+id+'"]');
    // Stale: listed apart as no result, never a decision.
    const stale = card(':stale-request');
    await stale.waitFor();
    assert.equal(await p.locator('#oks-unreported').textContent(), 'No result reported');
    assert.equal((await stale.locator('[data-unreported]').textContent()).trim(), 'No result reported · your laptop is suspended until it updates AgentNet');
    assert.equal(await stale.getByText(/Decide on/).count(), 0, 'a stale word is no decision');
    assert.equal(await stale.getByText('No result', {exact:true}).count(), 1, 'tagged as no result');
    // Running: work, never a decision.
    const running = card(':running-request');
    await running.getByText(/^Running on your laptop\./i).waitFor();
    assert.equal(await running.getByText(/Decide on/).count(), 0, 'running is no decision');
    // Current awaiting: decided on the laptop.
    await card(':awaiting-request').getByText(/^Decide on your laptop/i).waitFor();
    assert.match(await p.locator('header').first().textContent(), /OKs/);
    assert.equal(await p.getByText(/things? needs? you/).count(), 0, 'nothing is counted as needing this device');
    // A suspended device's copy says so; the headline is everyone else's.
    await card(':stale-request').click();
    const bubble = p.locator('[data-mid="tolerance-msg"]');
    await bubble.waitFor();
    await p.getByText(/Delivered · Bob’s phone is suspended until it updates AgentNet/).first().waitFor();
    await p.getByText(/Delivered · Bob’s phone is suspended/).first().click();
    const sheet = p.getByRole('dialog');
    await sheet.locator('[data-suspended]').getByText('Bob’s phone is suspended until it updates AgentNet').waitFor();
    await p.keyboard.press('Escape');
    await sheet.waitFor({state:'detached'});
    // Not sent to one member's device: said under it, never "Delivered".
    const skipped = p.locator('[data-mid="skipped-msg"]');
    await skipped.waitFor();
    const under = p.getByText('Not sent · Not sent to Carol’s desk', {exact:true});
    await under.waitFor();
    assert.equal(await skipped.getByText(/Delivered/).count(), 0, 'never delivered while a member got nothing');
    await under.click();
    const details = p.getByRole('dialog');
    await details.getByText('Carol’s desk', {exact:true}).waitFor();
    await details.getByText('not sent: carol/desk\'s key changed').first().waitFor();
    assert.ok(await details.getByText('Not sent', {exact:true}).count() >= 1, 'that copy reads not sent');
    assert.deepEqual(errors, [], 'runtime errors');
    await ctx.close();
  } finally { await browser.close(); }
  console.log('peer tolerance rendered PASS: stale words read as no result, running as work, a current one as a decision; a suspended device named on its copy; a device not sent to named, never delivered');
})().catch(e=>{console.error(e);process.exit(1);});
