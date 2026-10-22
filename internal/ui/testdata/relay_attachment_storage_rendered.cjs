// Actual component, shared Sheet/Button and source CSS over a synthetic captured
// host. No relay account, real attachments, provider calls or generated assets.
const assert = require('node:assert/strict'), fs = require('node:fs'), os = require('node:os'), path = require('node:path'), http = require('node:http');
const { createRequire } = require('node:module'), { execFileSync } = require('node:child_process');
const webDir = path.resolve(__dirname, '../web'), web = createRequire(path.join(webDir, 'package.json'));
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'agentnet-relay-storage-render-'));
const entry = `
import React from 'react';
import {createRoot} from 'react-dom/client';
import {OwnedContext} from './src/owned';
import {RelayAttachmentStorage} from './src/features/RelayAttachmentStorage';
const base=()=>({can_admin:true,configured:true,connected:false,state:'local',local:{files:4,bytes:4096},remote:{files:0,bytes:0},pending:{files:4,bytes:4096},remote_only_files:0,migration:{running:false,files:0,bytes:0,total_files:0,total_bytes:0}});
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const react=createRoot(app),listeners=new Set();
window.fixture={calls:[],status:base(),base,active:true,platform:'browser',deferGet:new URLSearchParams(location.search).has('loading'),deferAction:false,error:'',nextConsent:'',set(v){this.status=v;this.emit();},emit(){for(const fn of listeners)fn({type:'change',seq:1});}};
const consent='https://accounts.google.com/o/oauth2/v2/auth?client_id=synthetic-public&state=synthetic';
function makeHost(id){return {platform:fixture.platform,listen(fn){listeners.add(fn);return()=>listeners.delete(fn)},async api(url,body){
 fixture.calls.push({id,url,body});if(url!=='/api/storage/attachments')throw Error('Unexpected endpoint '+url);
 if(!body){const result=structuredClone(fixture.status);if(fixture.deferGet){fixture.deferGet=false;return new Promise(resolve=>fixture.resolveRead=()=>resolve(result));}if(fixture.error)throw Error(fixture.error);return result;}
 const response=()=>{if(body.action==='connect')return {...fixture.status,consent_url:fixture.nextConsent||consent};
 if(body.confirm!==true)throw Error('Confirmation missing');
 if(body.action==='move'){fixture.status={...fixture.status,state:'migrating',migration:{running:true,files:0,bytes:0,total_files:fixture.status.local.files,total_bytes:fixture.status.local.bytes}};return structuredClone(fixture.status);}
 if(body.action==='disconnect'){if(fixture.status.remote_only_files)throw Error('Remote-only copies still exist');fixture.status={...fixture.status,connected:false,state:'local'};return structuredClone(fixture.status);}
 throw Error('Unknown action');};
 if(fixture.deferAction){fixture.deferAction=false;const result=response();return new Promise(resolve=>fixture.resolveAction=()=>resolve(result));}return response();
 }};}
let host=makeHost('one');
// Stable activity callback prevents a fixture rerender from creating an
// artificial effect change; real DriveSettings uses useCallback as well.
const isActive=()=>fixture.active;
const mount=()=>react.render(<OwnedContext.Provider value={{root,app,portals}}><RelayAttachmentStorage host={host} isActive={isActive}/></OwnedContext.Provider>);
fixture.switchHost=()=>{host=makeHost('two');mount();};
fixture.useAndroid=()=>{fixture.platform='android';host=makeHost('android');mount();};
mount();
`;
const bundle = web('esbuild').buildSync({ stdin: { contents: entry, resolveDir: webDir, loader: 'tsx' }, bundle: true, format: 'esm', platform: 'browser', target: 'es2022', jsx: 'automatic', define: { 'process.env.NODE_ENV': '"production"' }, write: false, logLevel: 'silent' }).outputFiles[0].text;
const cssFile = path.join(work, 'style.css');
execFileSync(path.join(webDir, 'node_modules/.bin/tailwindcss'), ['-i', path.join(webDir, 'src/styles.css'), '-o', cssFile, '--minify'], { cwd: webDir, stdio: 'pipe' });
const css = fs.readFileSync(cssFile, 'utf8') + '\nbody{margin:0}.fixture{max-width:720px;margin-inline:auto;padding:16px}.an-root{height:auto;min-height:100vh}.an-app{height:auto}';
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://fixture');
  if (req.method !== 'GET') { res.writeHead(405).end(); return; }
  if (url.pathname === '/') { res.setHeader('Content-Type', 'text/html'); res.end('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Relay attachment storage fixture</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css"><div id="root" class="an-root" data-theme="' + (url.searchParams.get('theme') === 'dark' ? 'dark' : 'light') + '"><div class="fixture"><div id="app" class="an-app"></div></div><div id="portals"></div></div><script type="module" src="/fixture.mjs"></script>'); return; }
  if (url.pathname === '/fixture.mjs') { res.setHeader('Content-Type', 'text/javascript'); res.end(bundle); return; }
  if (url.pathname === '/style.css') { res.setHeader('Content-Type', 'text/css'); res.end(css); return; }
  res.writeHead(404).end();
});

function contrast(a, b) {
  const luminance = rgb => {
    const v = rgb.match(/[\d.]+/g).slice(0, 3).map(Number).map(n => n / 255).map(n => n <= .04045 ? n / 12.92 : ((n + .055) / 1.055) ** 2.4);
    return v[0] * .2126 + v[1] * .7152 + v[2] * .0722;
  };
  const x = luminance(a), y = luminance(b);
  return (Math.max(x, y) + .05) / (Math.min(x, y) + .05);
}

(async () => {
  let browser;
  try {
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const origin = 'http://127.0.0.1:' + server.address().port;
    browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
    const focused = process.env.AGENTNET_RELAY_STORAGE_FOCUS === 'move';
    const memberOnly = process.env.AGENTNET_RELAY_STORAGE_FOCUS === 'member';
    const views = focused || memberOnly ? [[1280, 'dark'], [390, 'light']] : [1280, 390, 320].flatMap(width => ['light', 'dark'].map(theme => [width, theme]));
    for (const [width, theme] of views) {
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' }), page = await context.newPage(), errors = [], external = [];
      page.setDefaultTimeout(5000);
      await context.route('**/*', route => { if (new URL(route.request().url()).origin === origin) return route.continue(); external.push(route.request().url()); return route.abort(); });
      page.on('pageerror', e => errors.push(e.message));
      page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
      await page.goto(origin + '/?theme=' + theme + '&loading=1');
      const panel = page.locator('[data-part="relay-attachment-storage"]');
      const set = patch => page.evaluate(patch => fixture.set({ ...fixture.base(), ...patch }), patch);
      const mutations = () => page.evaluate(() => fixture.calls.filter(c => c.body));
      await panel.getByText('Reading attachment storage…', { exact: true }).waitFor();
      assert.equal(await panel.getByRole('button').count(), 1, 'unknown status grants no admin actions');
      assert.equal(await panel.getByRole('button').isDisabled(), true);
      await page.evaluate(() => fixture.resolveRead());
      await panel.getByText('Attachments stored on this relay', { exact: true }).waitFor();
      if (memberOnly) {
        for (const legacy of [true, false]) {
          await set({ can_admin: false, connected: true, state: 'connected', local: { files: legacy ? 123 : 0, bytes: legacy ? 125952 : 0 }, remote: { files: legacy ? 45 : 0, bytes: legacy ? 46080 : 0 }, pending: { files: legacy ? 78 : 0, bytes: legacy ? 79872 : 0 }, migration: { running: legacy, files: legacy ? 5 : 0, bytes: legacy ? 5120 : 0, total_files: legacy ? 78 : 0, total_bytes: legacy ? 79872 : 0 }, remote_only_files: legacy ? 45 : 0, account: { email: 'PRIVATE_ADMIN', name: 'PRIVATE_ADMIN' }, folder: { id: 'PRIVATE_FOLDER', name: 'PRIVATE_FOLDER' }, notice: 'PRIVATE_USAGE 123 files', consent_url: 'https://accounts.google.com/o/oauth2/v2/auth?client_id=private' });
          await panel.getByText('Google Drive connected', { exact: true }).waitFor();
          await panel.locator('dl').waitFor({ state: 'detached' });
          assert.equal(await panel.getByRole('button').count(), 1, 'member has refresh only');
          assert.equal(await panel.getByRole('link').count(), 0, 'member never receives a setup or consent link');
          assert.equal(await panel.getByRole('progressbar').count(), 0);
          assert.doesNotMatch(await panel.innerText(), /PRIVATE_|\b(?:0|123|45|78) (?:files|B)|KiB|Local relay copies|In Google Drive|Not yet in Drive|Last move/);
          assert.equal(await panel.getByRole('button', { name: 'Refresh storage status', exact: true }).isDisabled(), false);
        }
        await panel.getByRole('button', { name: 'Refresh storage status', exact: true }).focus();
        await page.keyboard.press('Enter');
        assert.equal((await mutations()).length, 0);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `relay-storage-member-${width}-${theme}.png`) });
        assert.deepEqual(errors, []); assert.deepEqual(external, []);
        await context.close();
        console.log(`${width} ${theme} member PASS: connection status and keyboard refresh only, no aggregate/zero totals, progress, operator details, notices, setup or mutations`);
        continue;
      }
      if (focused) {
        // Exact real pre-migration shape: local files exist, queue rows do not.
        await set({ connected: true, state: 'connected', pending: { files: 0, bytes: 0 }, remote: { files: 0, bytes: 0 } });
        const move = panel.getByRole('button', { name: 'Move existing attachments…', exact: true });
        await page.waitForFunction(() => [...document.querySelectorAll('button')].find(e => e.textContent === 'Move existing attachments…')?.disabled === false);
        await move.focus(); await page.keyboard.press('Enter');
        const dialog = page.getByRole('dialog', { name: 'Move existing relay attachments?', exact: true });
        await dialog.waitFor();
        await page.waitForFunction(() => { const d = document.querySelector('[role="dialog"]'), r = d?.getBoundingClientRect(); return r && r.left >= 0 && r.right <= innerWidth && r.top >= 0 && r.bottom <= innerHeight && getComputedStyle(d).opacity === '1'; });
        assert.match(await dialog.innerText(), /up to 4 files · 4 KiB/);
        assert.equal((await mutations()).length, 0, 'opening existing-local confirmation does not move files');
        if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `relay-storage-move-${width}-${theme}.png`) });
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
        await page.locator('[role="dialog"]').waitFor({ state: 'detached' });
        assert.equal(await move.evaluate(e => e === document.activeElement), true);
        await move.click(); await dialog.waitFor();
        await dialog.getByRole('button', { name: 'Move attachments', exact: true }).click();
        await page.locator('[role="dialog"]').waitFor({ state: 'detached' });
        assert.deepEqual((await mutations()).at(-1).body, { action: 'move', confirm: true });
        await panel.getByRole('progressbar').waitFor();
        assert.equal(await panel.getByRole('progressbar').getAttribute('max'), '4096');
        // Verified Drive objects can need their account even with local copies.
        await set({ connected: true, state: 'connected', pending: { files: 0, bytes: 0 }, remote_only_files: 0, remote: { files: 2, bytes: 2048 } });
        await panel.getByText(/2 files retained in Drive still need this account/).waitFor();
        assert.equal(await panel.getByRole('button', { name: 'Disconnect Drive…', exact: true }).isDisabled(), true);
        assert.equal(await page.getByRole('dialog', { includeHidden: true }).count(), 0, 'no closed-sheet contents remain in the normal view');
        assert.doesNotMatch(await page.locator('#root').innerText(), /Move existing relay attachments\?/);
        if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `relay-storage-${width}-${theme}.png`) });
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
        // The same confirmed action retries a linked account after an outage.
        await set({ connected: true, state: 'offline', pending: { files: 0, bytes: 0 } });
        const retry = panel.getByRole('button', { name: 'Retry moving attachments…', exact: true });
        await page.waitForFunction(() => [...document.querySelectorAll('button')].find(e => e.textContent === 'Retry moving attachments…')?.disabled === false);
        await retry.click(); await dialog.waitFor();
        await dialog.getByRole('button', { name: 'Move attachments', exact: true }).click();
        await page.locator('[role="dialog"]').waitFor({ state: 'detached' });
        assert.equal((await mutations()).length, 2);
        assert.deepEqual((await mutations()).at(-1).body, { action: 'move', confirm: true });
        assert.deepEqual(errors, []); assert.deepEqual(external, []);
        await context.close();
        console.log(`${width} ${theme} focused Move PASS: local>0/pending=0, explicit upper-bound confirmation, cancel/focus, verified progress, retained-Drive disconnect guard, offline retry, no leftover dialog text`);
        continue;
      }
      assert.equal((await mutations()).length, 0);
      assert.equal(await page.locator('input,select').count(), 0, 'no project/client-ID or personal Google setup');
      // Member views strip even erroneously included private operator data.
      await set({ can_admin: false, account: { email: 'PRIVATE_ADMIN_EMAIL', name: 'PRIVATE_ADMIN_NAME' }, folder: { id: 'PRIVATE_FOLDER', name: 'PRIVATE_FOLDER' } });
      await panel.getByRole('button', { name: 'Connect Google Drive', exact: true }).waitFor({ state: 'detached' });
      assert.doesNotMatch(await panel.innerText(), /PRIVATE_ADMIN|PRIVATE_FOLDER/);
      assert.equal(await panel.getByRole('button').count(), 1, 'members only have explicit status refresh');
      for (const state of ['not_configured', 'needs_reconnect', 'offline']) {
        await set({ can_admin: false, state, connected: state !== 'not_configured', configured: state !== 'not_configured' });
        await page.waitForFunction(state => document.querySelector('[data-part="relay-attachment-storage"]').textContent.includes(state === 'not_configured' ? 'needs operator setup' : state === 'offline' ? 'is unavailable' : 'needs reconnection'), state);
        assert.equal(await panel.getByRole('link', { name: 'Continue to Google' }).count(), 0);
        assert.equal(await panel.getByRole('button').count(), 1);
      }
      // Admin connection is a POST followed by a fresh, bounded link gesture.
      await set({});
      const connect = panel.getByRole('button', { name: 'Connect Google Drive', exact: true });
      await page.evaluate(() => { fixture.deferAction = true; fixture.resolveAction = null; });
      await connect.focus(); await page.keyboard.press('Enter');
      await panel.getByRole('button', { name: 'Starting connection…', exact: true }).waitFor();
      assert.equal(await panel.getByRole('button', { name: 'Starting connection…', exact: true }).isDisabled(), true);
      await page.evaluate(() => { fixture.emit(); fixture.resolveAction(); });
      const consent = panel.getByRole('link', { name: 'Continue to Google', exact: true });
      await consent.waitFor();
      assert.equal(await consent.getAttribute('target'), '_blank');
      assert.match(await consent.getAttribute('href'), /^https:\/\/accounts\.google\.com\/o\/oauth2\/v2\/auth\?/);
      assert.deepEqual((await mutations()).at(-1).body, { action: 'connect' });
      assert.equal(external.length, 0, 'starting consent never loads Google inside the page');
      await set({ connected: true, state: 'connected', remote: { files: 2, bytes: 2048 }, account: { email: 'synthetic-admin@example.invalid', name: 'Synthetic admin' }, folder: { id: 'synthetic', name: 'A deliberately long relay attachment folder name that must wrap on small screens' } });
      await consent.waitFor({ state: 'detached' });
      const move = panel.getByRole('button', { name: 'Move existing attachments…', exact: true });
      await move.focus(); await page.keyboard.press('Enter');
      const dialog = page.getByRole('dialog', { name: 'Move existing relay attachments?', exact: true });
      await dialog.waitFor();
      await page.waitForFunction(() => {
        const d = document.querySelector('[role="dialog"]'), r = d?.getBoundingClientRect();
        return r && r.left >= 0 && r.right <= innerWidth && r.top >= 0 && r.bottom <= innerHeight && getComputedStyle(d).opacity === '1';
      });
      assert.equal((await mutations()).length, 1, 'opening confirmation does not migrate');
      const moveNow = dialog.getByRole('button', { name: 'Move attachments', exact: true });
      if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `relay-storage-move-${width}-${theme}.png`) });
      await moveNow.focus(); await page.keyboard.press('Tab');
      assert.equal(await dialog.getByRole('button', { name: 'Close', exact: true }).evaluate(e => e === document.activeElement), true, 'Tab stays in the shared modal');
      await page.keyboard.press('Escape'); await dialog.waitFor({ state: 'hidden' });
      assert.equal(await move.evaluate(e => e === document.activeElement), true, 'keyboard dismissal returns focus');
      await move.click(); await dialog.waitFor(); await moveNow.click();
      await panel.getByRole('progressbar', { name: 'Existing attachment migration' }).waitFor();
      assert.deepEqual((await mutations()).at(-1).body, { action: 'move', confirm: true });
      await set({ connected: true, state: 'migrating', migration: { running: true, files: 2, bytes: 2048, total_files: 4, total_bytes: 4096 } });
      await page.waitForFunction(() => document.querySelector('progress')?.value === 2048);
      assert.equal(await move.isDisabled(), true);
      assert.equal(await panel.getByRole('button', { name: 'Disconnect Drive…', exact: true }).isDisabled(), true);
      await set({ connected: true, state: 'connected', remote_only_files: 3, remote: { files: 3, bytes: 3072 } });
      await panel.getByText(/3 files have their only relay copy in Drive/).waitFor();
      assert.equal(await panel.getByRole('button', { name: 'Disconnect Drive…', exact: true }).isDisabled(), true);
      if (process.env.AGENTNET_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.AGENTNET_SCREENSHOTS, `relay-storage-${width}-${theme}.png`) });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      const colors = await panel.evaluate(root => {
        const background = e => { for (let node = e; node; node = node.parentElement) { const color = getComputedStyle(node).backgroundColor; if (color !== 'rgba(0, 0, 0, 0)') return color; } return 'rgb(255, 255, 255)'; };
        return [...root.querySelectorAll('p,dt,dd,span,a,button:not([disabled])')].filter(e => e.textContent.trim()).map(e => ({ text: getComputedStyle(e).color, background: background(e) }));
      });
      for (const c of colors) assert.ok(contrast(c.text, c.background) >= 4.5, 'text contrast ' + JSON.stringify(c));
      // Explicit refresh and host changes replace stale reads, without polling.
      await page.evaluate(() => { fixture.deferGet = true; fixture.emit(); });
      await page.waitForFunction(() => typeof fixture.resolveRead === 'function');
      await set({ state: 'offline', connected: true });
      await panel.getByText('Google Drive is unavailable', { exact: true }).waitFor();
      await page.evaluate(() => fixture.resolveRead());
      assert.equal(await panel.getByText('Google Drive is unavailable', { exact: true }).count(), 1, 'stale read cannot overwrite current state');
      await page.evaluate(() => { fixture.error = 'Synthetic provider is unavailable'; fixture.emit(); });
      await panel.getByRole('alert').waitFor();
      await page.evaluate(() => { fixture.error = ''; });
      await panel.getByRole('button', { name: 'Refresh storage status', exact: true }).click();
      await panel.getByRole('alert').waitFor({ state: 'detached' });
      const reads = await page.evaluate(() => fixture.calls.length);
      await page.waitForTimeout(150);
      assert.equal(await page.evaluate(() => fixture.calls.length), reads, 'idle panel makes no periodic requests');
      // A modal rechecks new safety state before issuing disconnect.
      await set({ connected: true, state: 'connected' });
      await panel.getByRole('button', { name: 'Disconnect Drive…', exact: true }).click();
      const disconnect = page.getByRole('dialog', { name: 'Disconnect this relay’s Drive?', exact: true });
      await disconnect.waitFor();
      await set({ connected: true, state: 'connected', remote_only_files: 1 });
      await page.waitForFunction(() => [...document.querySelectorAll('[role="dialog"] button')].find(e => e.textContent === 'Disconnect Drive')?.disabled);
      await disconnect.getByRole('button', { name: 'Cancel', exact: true }).click();
      await disconnect.waitFor({ state: 'hidden' });
      await set({ connected: true, state: 'connected' });
      await panel.getByRole('button', { name: 'Disconnect Drive…', exact: true }).click();
      await disconnect.getByRole('button', { name: 'Disconnect Drive', exact: true }).click();
      await panel.getByText('Attachments stored on this relay', { exact: true }).waitFor();
      assert.deepEqual((await mutations()).at(-1).body, { action: 'disconnect', confirm: true });
      // A response for the old captured workspace cannot reveal a consent link.
      await page.evaluate(() => { fixture.deferAction = true; fixture.resolveAction = null; });
      await connect.click();
      await page.waitForFunction(() => typeof fixture.resolveAction === 'function');
      await page.evaluate(() => { fixture.status = { ...fixture.base(), can_admin: false }; fixture.switchHost(); });
      await page.waitForFunction(() => {
        const buttons = document.querySelector('[data-part="relay-attachment-storage"]').querySelectorAll('button');
        return fixture.calls.some(c => c.id === 'two' && !c.body) && buttons.length === 1 && !buttons[0].disabled;
      });
      await page.evaluate(() => fixture.resolveAction());
      await page.evaluate(() => new Promise(requestAnimationFrame));
      assert.equal(await consent.count(), 0);
      assert.equal(await panel.getByRole('button').count(), 1);
      // Android uses main-frame HTTPS navigation for the system browser guard.
      await page.evaluate(() => { fixture.status = fixture.base(); fixture.useAndroid(); });
      await connect.click(); await consent.waitFor();
      assert.equal(await consent.getAttribute('target'), '_self');
      const beforeRestart = (await mutations()).length;
      await panel.getByRole('button', { name: 'Start again', exact: true }).click();
      await consent.waitFor();
      assert.equal((await mutations()).length, beforeRestart + 1, 'expired or abandoned consent can start again');
      const inactiveCalls = await page.evaluate(() => { fixture.active = false; return fixture.calls.length; });
      await page.evaluate(() => fixture.emit());
      assert.equal(await page.evaluate(() => fixture.calls.length), inactiveCalls, 'inactive captured workspace makes no further calls');
      assert.deepEqual(errors, []); assert.deepEqual(external, []);
      assert.ok((await page.evaluate(() => fixture.calls)).every(c => c.url === '/api/storage/attachments'));
      await context.close();
      console.log(`${width} ${theme} PASS: members status-only; admin consent/confirmations; progress; remote-only and stale-response guards; keyboard, overflow, no polling/provider calls`);
    }
  } finally {
    await browser?.close();
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(work, { recursive: true, force: true });
  }
})().catch(e => { console.error(e); process.exitCode = 1; });
