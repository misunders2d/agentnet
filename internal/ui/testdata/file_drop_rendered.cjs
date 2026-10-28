// Real shipped skins and their normal composer/attachment path. Fictional
// browser files only; this is not a native Linux file-manager qualification.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require(process.env.AGENTNET_PLAYWRIGHT);
const assets = path.resolve(__dirname, '../static');
const collection = process.env.AGENTNET_DROP_SKINS;
const skins = collection ? ['comic', 'classic', 'zoom', 'holonet'] : ['comic'];
const boot = `
const source='c'.repeat(64),other='d'.repeat(64),skin=new URL(location).searchParams.get('skin');
const me={person:'a'.repeat(32),label:'Atlas',address:'atlas/laptop',fingerprint:'11111111-11111111-11111111-11111111',state:'self',devices:[]};
const peer={person:'b'.repeat(32),label:'Briar',address:'briar/desk',fingerprint:'22222222-22222222-22222222-22222222',state:'pinned',devices:[]};
const chat=id=>({id,kind:'group',title:id===source?'Drop fixture':'Other chat',peer,role:'member',members:[me,peer],agents:[],guests:[],topics:[],messages:[]});
const threads={[source]:chat(source),[other]:chat(other)};
let opened,changed;const f=window.fixture={uploads:[],writes:[],open:id=>opened(id,'conversation')};
const overview={version:'fixture',seq:1,topic_list:true,me,person:me,persons:true,groups:true,files:{max_file:1024,max_count:10},people:[peer],threads:[],topics:[],dms:[threads[source],threads[other]],directory:{current:true,members:[]},review:[],quarantine:[],links:[]};
f.disable=()=>{overview.files=null;overview.seq++;changed({type:'change',seq:overview.seq})};
f.enable=()=>{overview.files={max_file:1024,max_count:10};overview.seq++;changed({type:'change',seq:overview.seq})};
const host={version:1,platform:'daemon',workspace:{id:'drop-fixture',name:'Drop fixture',state:'enrolled',address:me.address},skins:[],onSkinsChange(){return()=>{}},onOpen:fn=>opened=fn,listen(fn){changed=fn;return()=>{}},
 stage:async file=>{f.uploads.push({name:file.name,size:file.size,bytes:[...new Uint8Array(await file.arrayBuffer())]});return 'staged-'+f.uploads.length},
 api:async(p,b)=>{if(p.startsWith('/api/overview'))return structuredClone(overview);if(p.startsWith('/api/dm?'))return structuredClone(threads[new URL(p,location).searchParams.get('id')]);if(p==='/api/dm/send')return new Promise((resolve,reject)=>f.writes.push({body:b,resolve,reject}));if(p.startsWith('/api/agents'))return{agents:[]};if(p.includes('/groups/invitations'))return[];if(p.startsWith('/api/typing/status'))return{send:false,scopes:[]};return{};}};
const base='/skin/'+skin+'/',manifest=await(await fetch(base+'skin.json')).json();
if(manifest.document){const style=new CSSStyleSheet();style.replaceSync((await(await fetch(base+manifest.document)).text()).replaceAll('url(m/','url('+base+'m/'));document.adoptedStyleSheets=[style]}
const shadow=document.querySelector('#skin').attachShadow({mode:'open'}),style=new CSSStyleSheet();style.replaceSync(await(await fetch(base+manifest.style)).text());shadow.adoptedStyleSheets=[style];const root=document.createElement('div');root.className='skin-root';shadow.append(root);
await(await import(base+manifest.entry)).mount(root,host);window.ready=true;
`;
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1');
  if (url.pathname === '/') { res.setHeader('Content-Type', 'text/html'); return res.end('<!doctype html><title>File drop fixture</title><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0}#skin{height:100dvh}</style><div id="skin"></div><script type="module" src="/boot.mjs"></script>'); }
  if (url.pathname === '/boot.mjs') { res.setHeader('Content-Type', 'text/javascript'); return res.end(boot); }
  const match = url.pathname.match(/^\/skin\/(comic|classic|zoom|holonet)\/(.+)$/);
  if (!match) { res.writeHead(404); return res.end(); }
  const root = match[1] === 'comic' ? path.join(assets, 'skins/comic') : path.join(collection, match[1]);
  const file = path.resolve(root, match[2]);
  if (!file.startsWith(root + path.sep)) { res.writeHead(403); return res.end(); }
  try { res.setHeader('Content-Type', file.endsWith('.mjs') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/json'); res.end(fs.readFileSync(file)); }
  catch { res.writeHead(404); res.end(); }
});

// Exercise real DOM listeners, including an item-only file transfer whose
// types/FileList do not advertise the browser-owned File object.
async function drop(field, mode, names = ['notes.txt']) {
  return field.evaluate((el, { mode, names }) => {
    const transfer = new DataTransfer();
    if (!['empty', 'url', 'text', 'link'].includes(mode)) for (const name of names) transfer.items.add(new File([new Uint8Array(mode === 'oversize' ? 2048 : mode === 'zero' ? [] : [1, 2, 3])], name, { type: 'text/plain' }));
    transfer.setData('text/plain', '/fictional/private/notes.txt');
    if (mode === 'url') transfer.setData('text/uri-list', 'file:///fictional/private/notes.txt');
    if (mode === 'link') transfer.setData('text/uri-list', 'https://fictional.invalid/notes');
    if (mode === 'items') { Object.defineProperty(transfer, 'files', { value: new DataTransfer().files }); Object.defineProperty(transfer, 'types', { value: ['text/plain'] }); }
    if (mode === 'empty') Object.defineProperty(transfer, 'types', { value: ['Files', 'text/plain'] });
    const event = new DragEvent('drop', { dataTransfer: transfer, bubbles: true, cancelable: true, composed: true });
    el.dispatchEvent(event);
    return event.defaultPrevented;
  }, { mode, names });
}

(async () => {
  let browser;
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    browser = await chromium.launch({ executablePath: process.env.AGENTNET_CHROMIUM, headless: true, args: ['--no-sandbox'] });
    for (const skin of skins) for (const width of [1280, 390]) {
      const page = await browser.newPage({ viewport: { width, height: 850 }, reducedMotion: 'reduce' });
      const errors = []; page.on('pageerror', error => errors.push(String(error)));
      await page.goto('http://127.0.0.1:' + server.address().port + '/?skin=' + skin);
      await page.waitForFunction(() => window.ready);
      await page.evaluate(() => fixture.open('c'.repeat(64)));
      if (skin === 'zoom') await page.getByRole('button', { name: 'Write in this group…', exact: true }).click();
      const field = skin === 'comic' ? page.locator('[data-part=conversation][aria-label="Drop fixture"] textarea') : skin === 'zoom' ? page.locator('#skin #write-body') : page.locator('#skin textarea').last();
      await field.fill('Keep this draft');
      assert.equal(await drop(field, 'normal', ['one.txt', 'two.txt']), true);
      await page.getByRole('button', { name: 'Remove one.txt', exact: true }).waitFor();
      await page.getByRole('button', { name: 'Remove two.txt', exact: true }).waitFor();
      assert.equal(await field.inputValue(), 'Keep this draft');
      assert.deepEqual(await page.evaluate(() => [fixture.uploads.length, fixture.writes.length]), [0, 0], 'drop never stages or sends automatically');
      await page.getByRole('button', { name: 'Remove one.txt', exact: true }).click();
      assert.equal(await drop(field, 'items', ['item-only.txt']), true);
      await page.getByRole('button', { name: 'Remove item-only.txt', exact: true }).waitFor();
      if (skin === 'zoom') await page.locator('#skin #dialog-cancel').click();
      await page.evaluate(() => fixture.open('d'.repeat(64)));
      if (skin !== 'zoom') await page.getByRole('button', { name: 'Remove item-only.txt', exact: true }).waitFor({ state: 'hidden' });
      await page.evaluate(() => fixture.open('c'.repeat(64)));
      if (skin === 'zoom') await page.getByRole('button', { name: 'Write in this group…', exact: true }).click();
      await page.getByRole('button', { name: 'Remove item-only.txt', exact: true }).waitFor();
      assert.equal(await field.inputValue(), 'Keep this draft', 'draft and dropped files stay in their original conversation');
      assert.equal(await drop(field, 'empty'), true);
      await page.getByText('This drop did not include readable files. Use Attach to choose them.', { exact: true }).waitFor();
      assert.equal(await drop(field, 'url'), true);
      assert.equal(await field.inputValue(), 'Keep this draft', 'unreadable file URL never becomes draft text or filesystem access');
      assert.equal(await drop(field, 'text'), false, 'ordinary text keeps native drop handling');
      assert.equal(await drop(field, 'link'), false, 'ordinary web links keep native drop handling');
      assert.equal(await drop(field, 'zero', ['empty.txt']), true);
      await page.getByText('Folders and empty files can’t be attached.', { exact: true }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Remove empty.txt', exact: true }).count(), 0);
      assert.equal(await drop(field, 'oversize', ['too-large.txt']), true);
      await page.getByRole('button', { name: 'Remove too-large.txt', exact: true }).waitFor();
      await page.getByRole('button', { name: 'Remove too-large.txt', exact: true }).click();
      if (process.env.AGENTNET_DROP_SCREENSHOTS) await page.screenshot({ path: process.env.AGENTNET_DROP_SCREENSHOTS + '-' + skin + '-' + width + '.png' });
      await page.evaluate(() => fixture.disable());
      // A background limit/permission refresh cannot let a file fall through
      // to the textarea's default text/path insertion.
      if (skin === 'comic') {
        await page.getByRole('button', { name: 'Add to message', exact: true }).click();
        await page.locator('[role=menuitem][aria-disabled=true]').filter({ hasText: 'Photo or file' }).waitFor();
        await page.keyboard.press('Escape');
      } else await page.waitForFunction(() => document.querySelector('#skin').shadowRoot.querySelector('#attach').hidden);
      assert.equal(await drop(field, 'normal', ['disabled.txt']), true);
      await page.getByText('Files can’t be attached here right now.', { exact: true }).waitFor();
      assert.equal(await page.getByRole('button', { name: 'Remove disabled.txt', exact: true }).count(), 0);
      await page.evaluate(() => fixture.enable());
      if (skin !== 'comic') await page.waitForFunction(() => !document.querySelector('#skin').shadowRoot.querySelector('#attach').hidden);
      if (skin === 'zoom') await page.locator('#skin #dialog-ok').click();
      else await page.locator('#skin form:has(textarea) button[type=submit]:visible').first().click();
      await page.waitForFunction(() => fixture.writes.length === 1);
      assert.deepEqual(await page.evaluate(() => fixture.uploads), [{ name: 'two.txt', size: 3, bytes: [1, 2, 3] }, { name: 'item-only.txt', size: 3, bytes: [1, 2, 3] }]);
      assert.equal(await page.evaluate(() => fixture.writes[0].body.body), 'Keep this draft');
      assert.equal(await page.evaluate(() => fixture.writes[0].body.files.length), 2);
      assert.deepEqual(errors, []);
      console.log('FILE DROP PASS ' + skin + ' ' + width);
      await page.close();
    }
  } finally { await browser?.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
