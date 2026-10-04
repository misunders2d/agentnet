// Notebook-specific captured-host journey, bound to immutable package bytes.
// No real peers, services, account/browser profiles or agent execution.
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { pathToFileURL } = require('node:url');
const assert = require('node:assert/strict');
const assets = path.resolve(__dirname, '../static');
const args = process.argv.slice(2);
let notebook = path.resolve(__dirname, '../../../examples/skins/notebook');
if (args.length) {
  if (args.length !== 2 || args[0] !== '--skin-dir') throw Error('Usage: node notebook_conformance_check.cjs [--skin-dir <Notebook package directory>]');
  notebook = path.resolve(args[1]);
}
const evidence = { adapter: 'notebook-device-thread-drafts-v1', host_version: 1, provider: 'captured synthetic host + WorkspaceShell.state', pass: false, status: 'failed', viewports: [], limits: ['Notebook-specific selectors; other packages unsupported', 'Captured provider, not live/native/browser-engine transport parity', 'Chromium at desktop/mobile viewport sizes, not mobile OS or other browsers', 'Advisory digest-bound compatibility evidence; full-trust code, no security certification', 'In-memory drafts and browser leave prompt; no crash durability'] };

const bootstrap = `import {mount,unmount} from '/entry.mjs';
import {WorkspaceShell} from '/assets/workspaces.mjs';
window.sent=[];window.sentWorkspaces=[];window.stageWait=null;window.sendWait=null;window.stageCount=0;window.apiStarted=0;window.outcome='custody';window.rejectSend=false;
const shell=new WorkspaceShell(), ids={A:'default',B:'b'.repeat(32)}, listeners=new Map();
for(const [name,id] of Object.entries(ids))shell.register({id,handle:name.toLowerCase().repeat(32),name:'Workspace '+name,endpoint:location.origin,address:'fixture/alice',state:'enrolled'});
window.shell=shell;window.ids=ids;window.active='A';
const workspaces={list:()=>shell.list(),state:id=>shell.state(id)};
const peers=id=>id===ids.A?{a:'fixture/bob',b:'fixture/carol'}:{a:'other/bob',b:'other/carol'};
const host=id=>({platform:'browser',version:1,workspace:{id},workspaces,
 stage:async file=>{stageCount++;if(stageWait)await stageWait;return {id:file.name};},
 listen:fn=>{listeners.set(id,fn);return()=>listeners.delete(id);},onOpen(){},
 api:async(p,b)=>{
  if(p==='/api/overview')return {me:{address:'fixture/alice'},threads:Object.entries(peers(id)).map(([id,peer])=>({id,peer,last:'Hello'}))};
  if(p.startsWith('/api/thread?')){const key=new URL(p,location.origin).searchParams.get('id');return {peer:peers(id)[key],messages:[{id:key.repeat(32),dir:'in',from:peers(id)[key],body:'Hello',kind:'message',state:'delivered'}]};}
  if(p.startsWith('/api/typing'))return b?{}:{current:true,supported:false,preferences:{send:false,show:false},entries:[]};
  if(p==='/api/send'){apiStarted++;if(sendWait)await sendWait;if(rejectSend)throw Error('Synthetic refusal');sent.push(b);sentWorkspaces.push(id);return {id:'sent'+sent.length,state:outcome};}
  throw Error('unexpected route '+p);
 }});
let root=document.querySelector('#root');
window.push=event=>listeners.get(ids[active])?.(event);
window.listenerCount=()=>listeners.size;
window.switchWorkspace=async name=>{
 ready=false;await unmount(root);root.remove();root=document.createElement('div');root.id='root';document.querySelector('#surface').append(root);
 active=name;shell.select(ids[name]);await mount(root,host(ids[name]));ready=true;
};
document.querySelector('#workspace').onchange=event=>switchWorkspace(event.target.value);
document.querySelector('#escape').onclick=()=>location.assign('/?skin=default');
document.querySelector('#reload').onclick=()=>location.reload();
await mount(root,host(ids.A));window.ready=true;`;

async function preparePackage() {
  // Existing canonical digest and manifest validation; no second hash format.
  const dir = fs.realpathSync(notebook), manifest = fs.readFileSync(path.join(dir, 'skin.json'));
  const m = JSON.parse(manifest.toString('utf8'));
  const files = ['skin.json', ...(Array.isArray(m.files) ? m.files : [])].map(name => {
    if (typeof name !== 'string') throw Error('Invalid package path');
    const full = path.resolve(dir, name);
    if (!full.startsWith(dir + path.sep)) throw Error('Package path escapes its directory');
    for (let p = full; p !== dir; p = path.dirname(p)) if (fs.lstatSync(p).isSymbolicLink()) throw Error('Package symlinks are unsupported');
    const bytes = name === 'skin.json' ? manifest : fs.readFileSync(full);
    return { name, size: bytes.length, arrayBuffer: async () => bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) };
  });
  const { prepare } = await import(pathToFileURL(path.join(assets, 'local-skins.mjs')).href);
  return prepare(files);
}

(async () => {
  const prepared = await preparePackage();
  evidence.package = { id: prepared.item.package_id, digest: prepared.item.digest, entry: prepared.item.entry, style: prepared.item.style, files: prepared.item.files };
  if (prepared.item.package_id !== 'notebook' || prepared.item.entry !== 'entry.mjs' || prepared.item.style !== 'style.css' || prepared.item.files.length !== 2 || !prepared.item.files.includes('entry.mjs') || !prepared.item.files.includes('style.css')) {
    evidence.status = 'unsupported'; evidence.error = 'This adapter exercises the Notebook example contract only; no generic skin conformance verdict.';
    console.log(JSON.stringify(evidence)); process.exitCode = 2; return;
  }
  const packageFiles = new Map(prepared.assets.map(f => [f.name, Buffer.from(f.bytes)]));
  const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT || 'playwright-core');
  const server = http.createServer((req, res) => {
    if (req.method !== 'GET') return res.writeHead(405).end();
    const url = new URL(req.url, 'http://fixture'), p = url.pathname;
    let body, type = 'text/javascript';
    if (p === '/') {
      type = 'text/html';
      body = '<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><link rel="stylesheet" href="/style.css">' +
        (url.searchParams.get('skin') === 'default' ? '<h1>Default fixture</h1>' : '<nav aria-label="Captured host controls"><label>Workspace <select id="workspace" aria-label="Workspace"><option>A</option><option>B</option></select></label> <button id="escape">Back to AgentNet</button> <button id="reload">Reload page</button></nav><main id="surface"><div id="root"></div></main><script type="module" src="/fixture.mjs"></script>');
    } else if (p === '/fixture.mjs') body = bootstrap;
    else if (packageFiles.has(p.slice(1))) { body = packageFiles.get(p.slice(1)); type = p.endsWith('.css') ? 'text/css' : 'text/javascript'; }
    else if (['/assets/typing.mjs', '/assets/workspaces.mjs'].includes(p)) body = fs.readFileSync(path.join(assets, path.basename(p)));
    else return res.writeHead(404).end();
    res.setHeader('Content-Type', type + '; charset=utf-8'); res.end(body);
  });
  let browser;
  try {
    await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
    const origin = 'http://127.0.0.1:' + server.address().port;
    browser = await chromium.launch({ headless: true, executablePath: process.env.AGENTNET_CHROMIUM || '/usr/bin/chromium' });
    evidence.browser = browser.version();
    for (const width of [390, 1280]) {
      const context = await browser.newContext({ viewport: { width, height: 900 } });
      context.setDefaultTimeout(10000);
      await context.addInitScript(() => {
        const add = window.addEventListener.bind(window), remove = window.removeEventListener.bind(window), listeners = new Set();
        window.addEventListener = (type, fn, ...rest) => { if (type === 'beforeunload') listeners.add(fn); return add(type, fn, ...rest); };
        window.removeEventListener = (type, fn, ...rest) => { if (type === 'beforeunload') listeners.delete(fn); return remove(type, fn, ...rest); };
        window.leaveListenerCount = () => listeners.size;
      });
      const page = await context.newPage(), errors = [], external = [], journeys = [], screenshots = [];
      const result = { width, height: 900, pass: false, journeys, screenshots, dialogs: { cancelled: 0, confirmed: 0, clean: 0 } };
      evidence.viewports.push(result);
      page.on('pageerror', e => errors.push(e.message));
      await context.route('**/*', route => {
        if (route.request().url().startsWith(origin + '/')) return route.continue();
        external.push(route.request().url()); return route.abort();
      });
      const shot = async name => {
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, 'no horizontal overflow');
        if (!process.env.AGENTNET_SCREENSHOTS) return;
        const p = path.join(process.env.AGENTNET_SCREENSHOTS, 'notebook-' + name + '-' + width + '.png');
        await page.screenshot({ path: p }); fs.chmodSync(p, 0o600); screenshots.push(p);
      };
      const cancelLeave = async button => {
        const shown = page.waitForEvent('dialog'), action = page.getByRole('button', { name: button, exact: true }).click();
        const dialog = await shown; assert.equal(dialog.type(), 'beforeunload'); await dialog.dismiss(); await action;
        assert.equal(new URL(page.url()).searchParams.get('skin'), null); result.dialogs.cancelled++;
      };
      const switchTo = async name => {
        await page.getByLabel('Workspace', { exact: true }).selectOption(name);
        await page.waitForFunction(name => ready && active === name, name);
        assert.equal(await page.evaluate(() => listenerCount()), 1, 'one active push subscription');
        assert.equal(await page.evaluate(() => leaveListenerCount()), 1, 'one leave guard across remount');
      };
      const message = page.getByLabel('Message', { exact: true });
      // On a phone the notebook shows its contents or one page: back to the contents first.
      const choose = async peer => { const back = page.getByRole('button', { name: '← Contents', exact: true }); if (await back.isVisible()) await back.click(); await page.getByRole('button', { name: peer + ' — Hello' }).click(); };
      const files = () => page.locator('input[type="file"]');
      const removeFiles = async () => { while (await page.getByRole('button', { name: 'Remove', exact: true }).count()) await page.getByRole('button', { name: 'Remove', exact: true }).first().click(); };
      await page.goto(origin); await page.waitForFunction(() => window.ready);
      assert.equal(await page.evaluate(() => leaveListenerCount()), 0, 'clean mount has no unload listener');
      await shot('contents');
      // Preserve the original async recipient/kind/reply and sent-only clearing regression.
      await choose('fixture/bob'); await message.fill('Bob file message');
      await files().setInputFiles({ name: 'note.txt', mimeType: 'text/plain', buffer: Buffer.from('synthetic bytes') });
      await page.evaluate(() => { stageWait = new Promise(resolve => { window.releaseStage = resolve; }); });
      await page.getByRole('button', { name: 'Send to fixture/bob', exact: true }).click();
      await page.waitForFunction(() => stageCount === 1);
      await choose('fixture/carol'); await message.fill('Carol task draft'); await page.getByLabel('Send as').selectOption('task');
      await page.evaluate(() => { releaseStage(); stageWait = null; });
      await page.waitForFunction(() => sent.length === 1 && !document.querySelector('button.send').disabled);
      assert.deepEqual(await page.evaluate(() => sent[0]), { to: 'fixture/bob', body: 'Bob file message', kind: 'message', reply_to: 'a'.repeat(32), files: [{ id: 'note.txt' }] });
      assert.equal(await message.inputValue(), 'Carol task draft'); assert.equal(await page.getByLabel('Send as').inputValue(), 'task');
      await choose('fixture/bob'); assert.equal(await message.inputValue(), ''); assert.equal(await page.getByLabel('Send as').inputValue(), 'message');
      assert.equal(await page.getByRole('button', { name: 'Remove', exact: true }).count(), 0);
      await choose('fixture/carol'); assert.equal(await message.inputValue(), 'Carol task draft'); await choose('fixture/bob');
      await message.fill('Keep this after refusal'); await page.evaluate(() => { rejectSend = true; });
      await page.getByRole('button', { name: 'Send to fixture/bob', exact: true }).click(); await page.getByText('Synthetic refusal', { exact: true }).waitFor();
      assert.equal(await message.inputValue(), 'Keep this after refusal');
      await page.evaluate(() => { rejectSend = false; outcome = 'queued'; });
      await page.getByRole('button', { name: 'Send to fixture/bob', exact: true }).click(); await page.getByText('Saved here; waiting to send.', { exact: true }).waitFor();
      assert.equal(await message.inputValue(), ''); await page.evaluate(() => push({ type: 'change' }));
      await page.waitForFunction(() => document.querySelector('textarea').value === '');
      await message.fill('First text'); await files().setInputFiles({ name: 'first.txt', mimeType: 'text/plain', buffer: Buffer.from('first') });
      await page.evaluate(() => { stageWait = new Promise(resolve => { window.releaseStage = resolve; }); });
      await page.getByRole('button', { name: 'Send to fixture/bob', exact: true }).click(); await page.waitForFunction(() => stageCount === 2);
      await message.fill('New text while sending'); await files().setInputFiles({ name: 'next.txt', mimeType: 'text/plain', buffer: Buffer.from('next') });
      await page.evaluate(() => { releaseStage(); stageWait = null; });
      await page.waitForFunction(() => sent.length === 3 && !document.querySelector('button.send').disabled);
      assert.equal(await message.inputValue(), 'New text while sending'); assert.equal(await page.getByRole('button', { name: 'Remove', exact: true }).count(), 1);
      assert.match(await page.locator('.clippings').innerText(), /next\.txt · 4 B/); assert.doesNotMatch(await page.locator('.clippings').innerText(), /first\.txt/);
      journeys.push('async recipient/kind/reply capture, independent conversations, refusal, queued and sent-only clearing');
      await shot('async-draft');
      // Same conversation ids in two memberships; native File objects live in host state.
      await removeFiles(); await message.fill('Workspace A exact draft'); await page.getByLabel('Send as').selectOption('task');
      await files().setInputFiles({ name: 'a.txt', mimeType: 'text/plain', buffer: Buffer.from('A bytes') });
      await switchTo('B'); await choose('other/bob');
      assert.equal(await message.inputValue(), ''); assert.equal(await page.getByLabel('Send as').inputValue(), 'message');
      assert.equal(await page.getByRole('button', { name: 'Remove', exact: true }).count(), 0);
      await cancelLeave('Back to AgentNet'); // clean B still protects dirty inactive A
      await message.fill('Workspace B exact draft'); await page.getByLabel('Send as').selectOption('question');
      await files().setInputFiles({ name: 'b.txt', mimeType: 'text/plain', buffer: Buffer.from('B bytes') });
      await switchTo('A');
      assert.equal(await message.inputValue(), 'Workspace A exact draft'); assert.equal(await page.getByLabel('Send as').inputValue(), 'task');
      assert.match(await page.locator('.clippings').innerText(), /a\.txt/); assert.doesNotMatch(await page.locator('.clippings').innerText(), /b\.txt/);
      assert.equal(await page.locator('button[aria-current="true"]').getAttribute('data-key'), 'device:a');
      assert.equal(await page.evaluate(async () => shell.state(ids.A).notebook.drafts.get('device:a').files[0].file.text()), 'A bytes');
      await cancelLeave('Back to AgentNet'); await cancelLeave('Reload page');
      assert.equal(await message.inputValue(), 'Workspace A exact draft'); assert.match(await page.locator('.clippings').innerText(), /a\.txt/);
      await shot('workspace-a');
      // A send outlives remount; returning to A neither retargets nor duplicates it.
      await page.evaluate(() => { stageWait = new Promise(resolve => { window.releaseStage = resolve; }); });
      await page.getByRole('button', { name: 'Give task to fixture/bob', exact: true }).click(); await page.waitForFunction(() => stageCount === 3);
      await switchTo('B'); assert.equal(await message.inputValue(), 'Workspace B exact draft'); await shot('workspace-b');
      await switchTo('A'); assert.equal(await page.locator('button.send').isDisabled(), true);
      await page.evaluate(() => { releaseStage(); stageWait = null; }); await page.waitForFunction(() => sent.length === 4 && !document.querySelector('button.send').disabled);
      assert.deepEqual(await page.evaluate(() => sent[3]), { to: 'fixture/bob', body: 'Workspace A exact draft', kind: 'task', reply_to: 'a'.repeat(32), files: [{ id: 'a.txt' }] });
      assert.equal(await page.evaluate(() => sentWorkspaces[3]), 'default'); assert.equal(await message.inputValue(), '');
      await switchTo('B'); assert.equal(await message.inputValue(), 'Workspace B exact draft'); assert.equal(await page.getByLabel('Send as').inputValue(), 'question');
      assert.equal(await page.evaluate(async () => shell.state(ids.B).notebook.drafts.get('device:a').files[0].file.text()), 'B bytes');
      await page.getByRole('button', { name: 'Ask other/bob', exact: true }).click(); await page.waitForFunction(() => sent.length === 5 && !document.querySelector('button.send').disabled);
      assert.equal(await page.evaluate(() => sentWorkspaces[4]), 'b'.repeat(32)); assert.equal(await page.evaluate(() => sent[4].to), 'other/bob');
      journeys.push('A/B/A host state with identical ids, exact target, File bytes and kind; send survives remount without duplication');
      // Clear every draft explicitly: clean pages remove their listener.
      for (const [workspace, prefix] of [['A', 'fixture'], ['B', 'other']]) {
        await page.getByLabel('Workspace', { exact: true }).selectOption(workspace); await page.waitForFunction(name => ready && active === name, workspace);
        for (const person of ['bob', 'carol']) { await choose(prefix + '/' + person); await message.fill(''); await removeFiles(); }
      }
      assert.equal(await page.evaluate(() => leaveListenerCount()), 0, 'no stale guard after all drafts are clean');
      await choose('other/bob');
      // A file alone and a send alone both protect the document.
      await files().setInputFiles({ name: 'only-file.txt', mimeType: 'text/plain', buffer: Buffer.from('') });
      await cancelLeave('Back to AgentNet'); await removeFiles(); assert.equal(await page.evaluate(() => leaveListenerCount()), 0);
      await message.fill('In-flight only'); await page.getByLabel('Send as').selectOption('message');
      await page.evaluate(() => { sendWait = new Promise(resolve => { window.releaseSend = resolve; }); });
      await page.getByRole('button', { name: 'Send to other/bob', exact: true }).click(); await page.waitForFunction(() => apiStarted === 7);
      await message.fill(''); await cancelLeave('Back to AgentNet');
      await page.evaluate(() => { releaseSend(); sendWait = null; }); await page.waitForFunction(() => sent.length === 6 && !document.querySelector('button.send').disabled);
      assert.equal(await page.evaluate(() => leaveListenerCount()), 0); journeys.push('native escape/reload cancellation retains drafts; inactive/file-only/in-flight protection; no stale listeners');
      let cleanDialogs = 0; const unexpected = async d => { cleanDialogs++; await d.dismiss(); };
      page.on('dialog', unexpected); await page.getByRole('button', { name: 'Back to AgentNet', exact: true }).click();
      await page.getByRole('heading', { name: 'Default fixture', exact: true }).waitFor(); page.off('dialog', unexpected); assert.equal(cleanDialogs, 0);
      journeys.push('clean escape has no warning');
      // Accepting the actual native dialog discards only this document's memory state.
      await page.goto(origin); await page.waitForFunction(() => ready); await choose('fixture/bob'); await message.fill('Explicitly discarded');
      await files().setInputFiles({ name: 'discard.txt', mimeType: 'text/plain', buffer: Buffer.from('discard') });
      const shown = page.waitForEvent('dialog'), leaving = page.getByRole('button', { name: 'Back to AgentNet', exact: true }).click();
      const dialog = await shown; assert.equal(dialog.type(), 'beforeunload'); await dialog.accept(); await leaving; result.dialogs.confirmed++;
      await page.getByRole('heading', { name: 'Default fixture', exact: true }).waitFor();
      await page.goto(origin); await page.waitForFunction(() => ready); await choose('fixture/bob');
      assert.equal(await message.inputValue(), ''); assert.equal(await page.getByRole('button', { name: 'Remove', exact: true }).count(), 0);
      assert.equal(await page.getByLabel('Send as').inputValue(), 'message'); assert.equal(await page.evaluate(() => leaveListenerCount()), 0);
      journeys.push('confirmed leave discards drafts; returning starts empty (no reload/crash persistence claim)');
      assert.deepEqual(errors, []); assert.deepEqual(external, []); result.pass = true;
      await context.close();
    }
    evidence.pass = true; evidence.status = 'passed';
  } finally {
    if (browser) await browser.close();
    if (server.listening) await new Promise(resolve => server.close(resolve));
  }
  console.log(JSON.stringify(evidence));
})().catch(error => { evidence.error = error.message; console.error(error); console.log(JSON.stringify(evidence)); process.exitCode = 1; });
