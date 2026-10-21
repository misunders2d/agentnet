// Actual injected download script and host CSS in Chromium; native IPC is
// deliberately mocked. This verifies rendered controls, not a native save dialog.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const script = fs.readFileSync(process.env.AGENTNET_DOWNLOAD_SCRIPT || 'desktop/src-tauri/src/download.js', 'utf8');
const hostCSS = fs.readFileSync('internal/ui/static/core.css', 'utf8');
const evidence = process.env.AGENTNET_DOWNLOAD_SCREENSHOTS;
if (evidence) fs.mkdirSync(evidence, { recursive: true, mode: 0o700 });
const cases = [
  { name: 'automatic-dark', system: 'dark' },
  { name: 'automatic-light', system: 'light' },
  { name: 'explicit-light', system: 'dark', theme: 'light' },
  { name: 'explicit-dark', system: 'light', theme: 'dark' },
];
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM, headless: true, args: ['--no-sandbox'] });
  const results = [];
  try {
    for (const config of cases) {
      const context = await browser.newContext({ colorScheme: config.system, viewport: { width: 390, height: 800 } });
      const page = await context.newPage(), errors = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
      await page.route('http://agentnet.local/**', route => route.fulfill(route.request().url().endsWith('/core.css')
        ? { contentType: 'text/css', body: hostCSS }
        : { contentType: 'text/html', headers: { 'Content-Security-Policy': "style-src 'self'" }, body: '<!doctype html><title>Native download controls</title><meta name="color-scheme" content="light dark"><link rel="stylesheet" href="/core.css"><body><p>Download fixture</p></body>' }));
      await page.goto('http://agentnet.local/');
      await page.evaluate(theme => {
        if (theme) document.documentElement.dataset.theme = theme;
        window.fixture = { calls: [] };
        window.__agentnetLinuxDownloads = true;
        window.__TAURI_INTERNALS__ = { invoke: (name, args) => {
          fixture.calls.push({ name, args });
          if (name === 'agentnet_download_blob') return new Promise((_resolve, reject) => { fixture.rejectDownload = reject; });
          if (name === 'agentnet_cancel_download') { fixture.rejectDownload('Download cancelled.'); return Promise.resolve(); }
          throw Error('Unexpected native IPC: ' + name);
        } };
      }, config.theme);
      await page.addScriptTag({ content: script });
      await page.evaluate(() => {
        const file = new File(['download bytes'], 'readable-controls.txt', { type: 'text/plain' });
        const anchor = document.createElement('a');
        anchor.href = URL.createObjectURL(file); anchor.download = file.name;
        document.body.append(anchor); anchor.click(); anchor.remove(); URL.revokeObjectURL(anchor.href);
      });
      const tray = page.getByRole('region', { name: 'Downloads', exact: true });
      await tray.waitFor();
      await page.waitForFunction(() => !!fixture.rejectDownload);
      const dismiss = tray.getByRole('button', { name: 'Dismiss', exact: true });
      const cancel = tray.getByRole('button', { name: 'Cancel download', exact: true });
      assert(await dismiss.isDisabled(), 'Dismiss remains disabled during transfer');
      assert(await cancel.isDisabled(), 'Cancel waits for the actual native saving callback');
      const colors = async locator => locator.evaluate(node => {
        const style = getComputedStyle(node), trayStyle = getComputedStyle(document.getElementById('agentnet-native-downloads'));
        const luminance = color => {
          const rgb = color.match(/[\d.]+/g).slice(0, 3).map(Number).map(v => v / 255).map(v => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
          return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
        };
        const ratio = (a, b) => { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
        return { color: style.color, background: style.backgroundColor, contrast: ratio(style.color, style.backgroundColor), outlineContrast: ratio(style.outlineColor, trayStyle.backgroundColor), outlineStyle: style.outlineStyle, outlineWidth: parseFloat(style.outlineWidth), opacity: style.opacity, scheme: trayStyle.colorScheme, width: node.getBoundingClientRect().width };
      });
      const disabled = await colors(dismiss);
      assert(disabled.contrast >= 4.5, `${config.name} disabled text contrast ${disabled.contrast}: ${JSON.stringify(disabled)}`);
      assert.equal(disabled.opacity, '1', 'disabled text keeps its readable contrast');
      assert.equal(disabled.scheme, config.theme || 'light dark', 'the tray supports automatic and explicit document themes');
      await page.evaluate(() => window.__agentnetDownloadProgress({ url: fixture.calls[0].args.url, state: 'saving', progress: 0.5 }));
      assert(await cancel.isEnabled());
      const enabled = await colors(cancel);
      assert(enabled.contrast >= 4.5, `${config.name} enabled text contrast ${enabled.contrast}`);
      await cancel.hover();
      const hover = await colors(cancel);
      assert(hover.contrast >= 4.5, `${config.name} hover text contrast ${hover.contrast}`);
      assert.notEqual(hover.background, enabled.background, 'hover state is visible');
      await page.mouse.move(0, 0);
      await page.keyboard.press('Tab');
      assert.equal(await cancel.evaluate(node => node === document.activeElement), true, 'keyboard reaches enabled Cancel and skips disabled Dismiss');
      const focus = await colors(cancel);
      assert.equal(focus.outlineStyle, 'solid');
      assert(focus.outlineWidth >= 2 && focus.outlineContrast >= 3, 'keyboard focus is visibly distinct from the tray');
      assert.equal(await tray.evaluate(node => node.getBoundingClientRect().right <= innerWidth && node.getBoundingClientRect().left >= 0), true, 'narrow tray fits viewport');
      if (evidence) await page.screenshot({ path: path.join(evidence, config.name + '-focus.png') });
      await page.keyboard.press('Enter');
      await page.getByText('Download cancelled.', { exact: true }).waitFor();
      assert.equal(await cancel.count(), 0, 'terminal cancellation removes Cancel');
      assert(await dismiss.isEnabled(), 'terminal cancellation enables Dismiss');
      assert.deepEqual(await page.evaluate(() => fixture.calls.map(call => call.name)), ['agentnet_download_blob', 'agentnet_cancel_download']);
      await dismiss.focus(); await page.keyboard.press('Enter');
      assert.equal(await tray.count(), 0, 'keyboard Dismiss removes the finished tray');
      for (const state of ['saved', 'failed']) {
        await page.evaluate(state => window.__agentnetDownloadProgress({ url: 'blob:fixture/' + state, name: state + '.txt', state }), state);
        assert(await tray.getByRole('button', { name: 'Dismiss', exact: true }).isEnabled());
        await tray.getByRole('button', { name: 'Dismiss', exact: true }).click();
        assert.equal(await tray.count(), 0, `${state} Dismiss removes the terminal row`);
      }
      assert.deepEqual(errors, [], 'no runtime errors');
      results.push({ theme: config.name, scheme: disabled.scheme, disabledContrast: disabled.contrast, enabledContrast: enabled.contrast, hoverContrast: hover.contrast, focusContrast: focus.outlineContrast, nativeCancel: true, keyboardDismiss: true });
      await context.close();
    }
    console.log(JSON.stringify({ ok: true, cases: results, nativeRuntime: false }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
