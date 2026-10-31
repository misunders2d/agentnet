// The skin gallery (static/skin-gallery.mjs) against a fake GitHub: only the
// fixed sources are read, every byte is checked against the catalog, the
// package digest must match before anything is stored, a package that needs
// a newer AgentNet is refused before download, and bad catalogs fail closed.
// Runs in Node with the real package preparation (local-skins.mjs prepare).
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { gallery, newer, updateInstalled, LATEST, RAW } from '../static/skin-gallery.mjs';
import { prepare } from '../static/local-skins.mjs';

const sha = (b) => createHash('sha256').update(b).digest('hex');
const enc = new TextEncoder();
const manifest = (id, extra = {}) => enc.encode(JSON.stringify({ api: 1, id, name: id === 'pulp' ? 'Pulp' : 'Diner', entry: 'entry.mjs', style: 'style.css', files: ['entry.mjs', 'style.css'], ...extra }));
const pkg = (id, extra) => ({ 'skin.json': manifest(id, extra), 'entry.mjs': enc.encode('export async function mount(root, host) { host.onOpen(() => {}); }'), 'style.css': enc.encode(':host { color: black; }') });
const asFiles = (files) => Object.entries(files).map(([name, bytes]) => ({ name, webkitRelativePath: name, size: bytes.length, arrayBuffer: async () => bytes.slice().buffer }));
const digest = async (files) => (await prepare(asFiles(files))).item.digest;
const png = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 1, 2, 3]);

const pulp = pkg('pulp', { agentnet: 'v0.8.25' }), diner = pkg('diner', { agentnet: 'v0.8.26' });
const entry = async (id, files, extra = {}) => ({
  id, name: id === 'pulp' ? 'Pulp' : 'Diner', description: 'A test skin.', version: '1.0.0', api: 1, digest: await digest(files),
  files: Object.entries(files).map(([path, b]) => ({ path, sha256: sha(b), size: b.length })),
  previews: [{ path: 'previews/desktop-light.png', sha256: sha(png), size: png.length, alt: 'Desktop, light' }], ...extra,
});
const catalog = { catalog: 1, skins: [await entry('pulp', pulp, { agentnet: 'v0.8.25' }), await entry('diner', diner, { agentnet: 'v0.8.26' }),
  { id: '../bad', name: 'Bad', api: 1 }, { id: 'nofiles', name: 'No files', api: 1, version: '1', digest: 'a'.repeat(64), files: [] }] };

function world({ tag = 'v1.2.0', release = {}, cat = catalog, tamper = null, length = null, files = { pulp, diner }, held = [], trusted = {} } = {}) {
  const asked = [];
  const fetcher = async (url, init) => {
    asked.push(url);
    assert.equal(init.credentials, 'omit'); assert.equal(init.redirect, 'error'); assert.equal(init.referrerPolicy, 'no-referrer');
    let body;
    if (url === LATEST) body = enc.encode(JSON.stringify({ tag_name: tag, draft: false, prerelease: false, ...release }));
    else if (url === RAW + tag + '/published/catalog.json') body = enc.encode(JSON.stringify(cat));
    else {
      const m = url.startsWith(RAW + tag + '/published/') && url.slice((RAW + tag + '/published/').length).match(/^([a-z]+)\/(.+)$/);
      if (!m) return new Response('no', { status: 404 });
      body = m[2] === 'previews/desktop-light.png' ? png : files[m[1]]?.[m[2]];
      if (!body) return new Response('no', { status: 404 });
      if (tamper && url.endsWith(tamper)) { body = body.slice(); body[0] ^= 1; }
    }
    return new Response(body, { status: 200, headers: length ? { 'Content-Length': String(length) } : {} });
  };
  const store = [...held];
  const local = { supported: () => true, prepare, catalog: async () => store.map((x) => ({ ...x })),
    install: async (fs) => { const { item } = await prepare(fs); const i = store.findIndex((x) => x.id === item.id); if (i >= 0) store[i] = item; else store.push(item); return item; } };
  let changed = 0;
  const trust = { get: (id) => trusted[id] ?? null, set: (id, d) => { trusted[id] = d; } };
  const g = gallery({ running: () => 'v0.8.25', changed: async () => { changed++; }, fetcher, local, trust });
  return { g, local, asked, store, trusted, changedCount: () => changed };
}

// Versions agree with protocol.Newer on the cases that matter here.
assert.equal(newer('v0.8.26', 'v0.8.25'), true);
assert.equal(newer('v0.8.25', 'v0.8.25-3-gabcdef1'), false);
assert.equal(newer('v9.0.0', 'dev'), false, 'an unstamped build never refuses');
assert.equal(newer('0.8.26', 'v0.8.25'), false);

{ // List: valid entries only, compatibility from the running version, nothing stored.
  const { g, asked, store } = world();
  const list = await g.list();
  assert.equal(list.release, 'v1.2.0');
  assert.deepEqual(list.skins.map((s) => [s.id, s.state]), [['pulp', 'install'], ['diner', 'incompatible']]);
  assert.equal(list.skins[1].agentnet, 'v0.8.26');
  assert.deepEqual(asked, [LATEST, RAW + 'v1.2.0/published/catalog.json'], 'only the fixed release and its catalog are read');
  assert.equal(store.length, 0);
  // A preview is shown only after its hash matched.
  const blob = await g.preview('pulp', 0);
  assert.equal(blob.type, 'image/png'); assert.deepEqual(new Uint8Array(await blob.arrayBuffer()), png);
  await assert.rejects(g.preview('pulp', 3), /does not exist/);
}

{ // Install: every file verified, digest matched, stored once, host told; then installed.
  const w = world();
  const id = await w.g.install('pulp');
  assert.equal(id, 'local:pulp');
  assert.equal(w.store.length, 1); assert.equal(w.store[0].digest, catalog.skins[0].digest); assert.equal(w.changedCount(), 1);
  assert.equal((await w.g.list()).skins[0].state, 'installed');
  // A newer published digest offers an update.
  w.store[0].digest = 'f'.repeat(64);
  assert.equal((await w.g.list()).skins[0].state, 'update');
}

{ // A package that needs a newer AgentNet is refused before any package file is read.
  const w = world();
  await assert.rejects(w.g.install('diner'), /needs AgentNet v0\.8\.26 or newer/);
  assert.ok(!w.asked.some((u) => u.includes('/published/diner/')), 'no download for an incompatible package');
  assert.equal(w.store.length, 0);
}

{ // A tampered file is refused and nothing is stored.
  const w = world({ tamper: '/published/pulp/entry.mjs' });
  await assert.rejects(w.g.install('pulp'), /^Error: A file of Pulp did not match the gallery\. Nothing was installed\.$/);
  assert.equal(w.store.length, 0); assert.equal(w.changedCount(), 0);
  const p = world({ tamper: 'previews/desktop-light.png' });
  await assert.rejects(p.g.preview('pulp', 0), /did not match/);
}

{ // A catalog whose digest does not describe its files is refused before storing.
  const bad = structuredClone(catalog); bad.skins[0].digest = '0'.repeat(64);
  const w = world({ cat: bad });
  await assert.rejects(w.g.install('pulp'), /Pulp did not match the gallery/);
  assert.equal(w.store.length, 0);
}

{ // Fail closed: odd tags, drafts, unreadable catalogs, oversized answers.
  for (const tag of ['../main', 'a b', '']) await assert.rejects(world({ tag }).g.list(), /no published release/);
  await assert.rejects(world({ release: { prerelease: true } }).g.list(), /no published release/);
  await assert.rejects(world({ cat: { catalog: 2, skins: [] } }).g.list(), /unreadable catalog/);
  await assert.rejects(world({ length: 10 << 20 }).g.list(), /larger than allowed/);
  const offline = gallery({ fetcher: async () => { throw new TypeError('offline'); }, local: { supported: () => true } });
  await assert.rejects(offline.list(), /could not be reached/);
  const busy = gallery({ fetcher: async () => new Response('', { status: 403 }), local: { supported: () => true } });
  await assert.rejects(busy.list(), /busy/);
  const unsupported = gallery({ local: { supported: () => false } });
  await assert.rejects(unsupported.list(), /cannot store skins/);
}

{ // Updates: every stored gallery skin with a newer compatible version, keeping
  // trust only when it named the exact version replaced (owner decision).
  const pulp2 = { ...pulp, 'entry.mjs': enc.encode('export async function mount(root, host) { host.onOpen(() => {}); } // 1.1') };
  const diner0 = { ...diner, 'skin.json': manifest('diner', { agentnet: 'v0.8.24' }) };
  const cat2 = { catalog: 1, skins: [await entry('pulp', pulp2, { agentnet: 'v0.8.25', version: '1.1.0' }), await entry('diner', diner, { agentnet: 'v0.8.26' })] };
  const old = (await prepare(asFiles(pulp))).item, oldDiner = (await prepare(asFiles(diner0))).item, fresh = cat2.skins[0].digest;
  const files = { pulp: pulp2, diner };
  const w = world({ cat: cat2, files, held: [old, oldDiner], trusted: { 'local:pulp': old.digest, 'local:diner': oldDiner.digest } });
  assert.deepEqual(await updateInstalled(w.g, w.local), ['pulp']);
  assert.equal(w.store.find((x) => x.id === 'local:pulp').digest, fresh);
  assert.equal(w.trusted['local:pulp'], fresh, 'trust given to the replaced version carries');
  assert.equal(w.store.find((x) => x.id === 'local:diner').digest, oldDiner.digest, 'a version that needs a newer AgentNet is not installed');
  assert.ok(!w.asked.some((u) => u.includes('/published/diner/')));
  assert.deepEqual(await updateInstalled(w.g, w.local), [], 'current versions are left alone');
  const never = world({ cat: cat2, files, held: [old] });
  assert.deepEqual(await updateInstalled(never.g, never.local), ['pulp']);
  assert.equal(never.trusted['local:pulp'], undefined, 'an untrusted skin stays untrusted');
  const other = world({ cat: cat2, files, held: [old], trusted: { 'local:pulp': 'e'.repeat(64) } });
  await other.g.install('pulp');
  assert.equal(other.trusted['local:pulp'], 'e'.repeat(64), 'trust in other bytes never carries');
  const none = world({ cat: cat2, files });
  assert.deepEqual(await updateInstalled(none.g, none.local), [], 'nothing is installed that was not');
  assert.equal(none.store.length, 0);
  assert.deepEqual(none.asked, [], 'a browser storing no skin never asks the gallery');
  const tampered = world({ cat: cat2, files, held: [old], trusted: { 'local:pulp': old.digest }, tamper: '/published/pulp/entry.mjs' });
  assert.deepEqual(await updateInstalled(tampered.g, tampered.local), []);
  assert.equal(tampered.store[0].digest, old.digest); assert.equal(tampered.trusted['local:pulp'], old.digest);
}

console.log('skin gallery check PASS: fixed sources, verified previews and files, digest before storing, version gate, fail-closed catalogs, updates keep exact trust');
