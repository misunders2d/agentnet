// Storage failures with the real package manager/gallery. The browser companion
// additionally corrupts Chromium's on-disk index and checks persistent bytes.
import assert from 'node:assert/strict';
import * as local from '../static/local-skins.mjs';
import { gallery, LATEST, RAW } from '../static/skin-gallery.mjs';
import { createHash } from 'node:crypto';

const origin = 'http://127.0.0.1:8123', prefix = 'agentnet-skin-package-v1-';
const ready = origin + '/local-skins/ready', cat = 'agentnet-skin-catalog-v1';
globalThis.location = { origin };
Object.defineProperty(globalThis, 'navigator', { value: { serviceWorker: {}, locks: { request: async (_, fn) => fn() } }, configurable: true });
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const pkg = (id, version = 1) => [new File([JSON.stringify({ api: 1, id, name: id, entry: 'entry.mjs', files: ['entry.mjs', 'style.css'], style: 'style.css' })], 'skin.json'),
  new File(['export const version = ' + version + ';'], 'entry.mjs'), new File(['h1 { color: black; }'], 'style.css')];
const key = request => typeof request === 'string' ? request : request.url || request.href;
class Cache {
  responses = new Map(); damaged = false; writes = 0;
  async match(request) {
    if (this.damaged) throw new DOMException('Unexpected internal error.', 'UnknownError');
    return this.responses.get(key(request))?.clone();
  }
  async keys() { return [...this.responses.keys()].map(url => new Request(url)); }
  async put(request, response) {
    this.writes++;
    if (this.writeError && (!this.failAfter || this.writes >= this.failAfter)) throw this.writeError;
    this.responses.set(key(request), response.clone());
  }
  async delete(request) { return this.responses.delete(key(request)); }
}
function world() {
  const store = new Map(), deleted = [], created = [];
  globalThis.caches = {
    async open(name) {
      if (!store.has(name)) {
        const cache = new Cache(); if(this.failName===name){cache.writeError=this.writeError;cache.failAfter=this.failAfter;}
        store.set(name, cache);created.push({name,cache});
      }
      if(this.openError)throw this.openError;
      return store.get(name);
    },
    async has(name) { return store.has(name); },
    async keys() { return [...store.keys()]; },
    async delete(name) {
      deleted.push(name);
      if (this.deleteError) throw this.deleteError;
      return store.delete(name);
    },
  };
  return { store, deleted, created };
}
{ // Same-digest repair is idempotent; a duplicate checks bytes without rewrites.
  const w = await seed(); w.store.get(prefix+w.damaged.digest).damaged = true;
  assert.deepEqual(await local.install(pkg('damaged')), w.damaged);
  const c=w.store.get(prefix+w.damaged.digest), writes=c.writes, deletions=w.deleted.length;
  assert.deepEqual(await local.install(pkg('damaged')), w.damaged);
  assert.equal(c.writes,writes);assert.equal(w.deleted.length,deletions);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
}
async function snapshot(store, name) {
  const c = store.get(name);
  return Promise.all([...c.responses].map(async ([url, r]) => [url, [...new Uint8Array(await r.clone().arrayBuffer())]]));
}
const assetURL = (item, name) => origin + '/local-skins/' + item.digest + '/' + name;
async function seed() {
  const w = world(), healthy = await local.install(pkg('healthy')), damaged = await local.install(pkg('damaged'));
  return { ...w, healthy, damaged, before: await snapshot(w.store, prefix + healthy.digest) };
}

{ // The observed Chromium failure no longer blocks a verified gallery update.
  const w = await seed(); w.store.get(prefix + w.damaged.digest).damaged = true;
  const files = pkg('damaged', 2), prepared = await local.prepare(files), published = {};
  for (const f of files) published[f.name] = new Uint8Array(await f.arrayBuffer());
  const skin = { id: 'damaged', name: 'damaged', version: '2.0.0', api: 1, digest: prepared.item.digest,
    files: Object.entries(published).map(([path, bytes]) => ({ path, size: bytes.length, sha256: sha(bytes) })) };
  const g = gallery({ fetcher: async url => {
    if (url === LATEST) return Response.json({ tag_name: 'v2.0.0' });
    if (url === RAW + 'v2.0.0/published/catalog.json') return Response.json({ catalog: 1, skins: [skin] });
    return new Response(published[url.slice((RAW + 'v2.0.0/published/damaged/').length)]);
  } });
  assert.equal((await g.list()).skins[0].state, 'install', 'unreadable package is offered for repair');
  assert.equal(await g.install('damaged'), w.damaged.id);
  assert.equal((await g.list()).skins[0].state, 'installed');
  assert.deepEqual(w.deleted, [prefix+w.damaged.digest], 'verified new bytes replace only the chosen unreadable package');
  assert.equal(w.store.has(prefix+w.damaged.digest), false, 'chosen unreadable old snapshot does not permanently consume quota');
  assert.deepEqual(await snapshot(w.store, prefix + w.healthy.digest), w.before, 'healthy bytes are exact');
  assert.equal((await local.catalog()).find(x => x.id === w.damaged.id).digest, prepared.item.digest);
}
{ // Parent negative: explicit removal of a corrupt chosen ID keeps unrelated bytes.
  const w=await seed();w.store.get(prefix+w.damaged.digest).damaged=true;
  const healthyCatalog=await w.store.get(cat).match(origin+'/local-skin-catalog/'+encodeURIComponent(w.healthy.id)).then(r=>r.text());
  await local.remove(w.damaged.id);
  assert.equal(w.store.has(prefix+w.damaged.digest),false);
  assert.equal(w.store.get(cat).responses.has(origin+'/local-skin-catalog/'+encodeURIComponent(w.damaged.id)),false);
  assert.deepEqual(w.deleted,[prefix+w.damaged.digest]);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  assert.equal(await w.store.get(cat).match(origin+'/local-skin-catalog/'+encodeURIComponent(w.healthy.id)).then(r=>r.text()),healthyCatalog);
  console.log('explicit corrupt chosen removal PASS');
}
{ // Parent negative: four tiny corrupt skins permit a verified NEW digest update.
  const w=world(),healthy=await local.install(pkg('healthy')),before=await snapshot(w.store,prefix+healthy.digest), damaged=[];
  for(let i=0;i<4;i++)damaged.push(await local.install(pkg('damaged-'+i)));
  for(const item of damaged)w.store.get(prefix+item.digest).damaged=true;
  const next=await local.install(pkg('damaged-0',2));
  assert.notEqual(next.digest,damaged[0].digest);
  assert.deepEqual(w.deleted,[prefix+damaged[0].digest]);
  for(const item of damaged.slice(1))assert.equal(w.store.get(prefix+item.digest).damaged,true);
  assert.deepEqual(await snapshot(w.store,prefix+healthy.digest),before);
  assert.equal((await local.catalog()).find(x=>x.id===next.id).digest,next.digest);
  console.log('four corrupt skins verified new-digest update PASS');
}
{ // Removing another chosen skin ignores unrelated corruption and preserves its entry.
  const w=await seed();w.store.get(prefix+w.damaged.digest).damaged=true;
  const before=await w.store.get(cat).match(origin+'/local-skin-catalog/'+encodeURIComponent(w.damaged.id)).then(r=>r.text());
  await local.remove(w.healthy.id);
  assert.deepEqual(w.deleted,[prefix+w.healthy.digest]);
  assert.equal(w.store.get(prefix+w.damaged.digest).damaged,true);
  assert.equal(await w.store.get(cat).match(origin+'/local-skin-catalog/'+encodeURIComponent(w.damaged.id)).then(r=>r.text()),before);
}
{ // Failed exact delete preserves the catalog recovery/removal action.
  const w=await seed();w.store.get(prefix+w.damaged.digest).damaged=true;
  const before=await snapshot(w.store,cat);caches.deleteError=new DOMException('unavailable','InvalidStateError');
  await assert.rejects(local.remove(w.damaged.id),/storage/i);
  assert.deepEqual(await snapshot(w.store,cat),before);
  assert.equal(w.store.get(prefix+w.damaged.digest).damaged,true);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
}
{ // Catalog deletion failure retains its exact entry; explicit removal retry converges.
  const w=await seed(), c=w.store.get(cat), before=await snapshot(w.store,cat), original=c.delete.bind(c);
  c.delete=async()=>{throw new DOMException('unavailable','InvalidStateError');};
  await assert.rejects(local.remove(w.damaged.id),/storage/i);
  assert.deepEqual(await snapshot(w.store,cat),before);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  c.delete=original;await local.remove(w.damaged.id);
  assert.equal(c.responses.has(origin+'/local-skin-catalog/'+encodeURIComponent(w.damaged.id)),false);
}
{ // Malformed ready metadata alone cannot erase usable old bytes on a failed update.
  const w=await seed(), c=w.store.get(prefix+w.damaged.digest);c.responses.set(ready,new Response('{'));
  const before=await snapshot(w.store,prefix+w.damaged.digest), metadata=await snapshot(w.store,cat);
  w.store.get(cat).writeError=new DOMException('unavailable','InvalidStateError');
  await assert.rejects(local.install(pkg('damaged',2)),/storage/i);
  assert.deepEqual(await snapshot(w.store,prefix+w.damaged.digest),before);
  assert.deepEqual(await snapshot(w.store,cat),metadata);
}
{ // Missing or malformed ready metadata repair retains verified files on failure.
  for(const marker of ['missing','malformed'])for(const error of ['QuotaExceededError','InvalidStateError']){
    const w=await seed(), name=prefix+w.damaged.digest, c=w.store.get(name);
    if(marker==='missing')c.responses.delete(ready);else c.responses.set(ready,new Response('{'));
    const before=await snapshot(w.store,name), metadata=await snapshot(w.store,cat), writes=c.writes;
    c.writeError=new DOMException('unavailable',error);caches.failName=name;caches.writeError=c.writeError;
    await assert.rejects(local.install(pkg('damaged')),/storage/i);
    assert.deepEqual(w.deleted,[],marker+': no deletion');
    assert.equal(w.store.get(name),c,marker+': original cache retained');
    assert.equal(c.writes,writes+1,marker+': only ready write attempted');
    assert.deepEqual(await snapshot(w.store,name),before);
    assert.deepEqual(await snapshot(w.store,cat),metadata);
    assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
    c.writeError=null;caches.failName='';assert.deepEqual(await local.install(pkg('damaged')),w.damaged);
  }
  console.log('missing/malformed ready marker failed-write preservation and explicit retry PASS');
}
{ // A ready marker does not excuse absent or changed target bytes on reinstall.
  for (const corrupt of ['missing', 'changed', 'metadata']) {
    const w = await seed(), c = w.store.get(prefix + w.damaged.digest), asset = assetURL(w.damaged, 'entry.mjs');
    if (corrupt === 'missing') c.responses.delete(asset);
    else if (corrupt === 'changed') c.responses.set(asset, new Response('corrupt bytes'));
    else c.responses.set(ready, new Response('{'));
    if(corrupt!=='changed')assert.equal((await local.catalog()).some(x=>x.id===w.damaged.id),false,'incomplete snapshot is offered for reinstall');
    assert.deepEqual(await local.install(pkg('damaged')), w.damaged, corrupt + ': original digest restored');
    assert.equal(await w.store.get(prefix + w.damaged.digest).match(asset).then(r => r.text()), 'export const version = 1;');
    assert.deepEqual(await snapshot(w.store, prefix + w.healthy.digest), w.before);
  }
}
{ // Failed update keeps the previous usable version/catalog; retry is explicit.
  const w = await seed(), files = pkg('damaged', 2), next = (await local.prepare(files)).item;
  caches.failName = prefix + next.digest; caches.writeError = new DOMException('full', 'QuotaExceededError');
  await assert.rejects(local.install(files), /Skin.*storage|Skin not stored/i);
  assert.equal((await local.catalog()).find(x => x.id === w.damaged.id).digest, w.damaged.digest);
  assert.deepEqual(await snapshot(w.store, prefix + w.healthy.digest), w.before);
  assert.equal(w.created.filter(x=>x.name===prefix+next.digest).reduce((n,x)=>n+x.cache.writes,0), 1, 'no automatic full-cache write retry');
  caches.failName = '';
  assert.deepEqual(await local.install(files), next, 'one subsequent explicit retry succeeds');
}
{ // Storage unavailability is not evidence for deleting a readable snapshot.
  const w = await seed(), c = w.store.get(prefix + w.healthy.digest);
  c.match = async () => { throw new DOMException('unavailable', 'InvalidStateError'); };
  await assert.rejects(local.install(pkg('newskin')), /storage/i);
  assert.deepEqual(w.deleted, []);
  assert.deepEqual(await snapshot(w.store, prefix + w.healthy.digest), w.before);
}
{ // Backend-wide open failure, even UnknownError, cannot erase a package.
  const w=await seed();caches.openError=new DOMException('unavailable','UnknownError');
  await assert.rejects(local.install(pkg('newskin')),/storage/i);
  assert.deepEqual(w.deleted,[]);
}
{ // Partial rebuild failure never publishes ready/catalog; the old version stays.
  const w=await seed(), files=pkg('damaged',2), next=(await local.prepare(files)).item;
  caches.failName=prefix+next.digest;caches.writeError=new DOMException('full','QuotaExceededError');caches.failAfter=2;
  await assert.rejects(local.install(files),/storage/i);
  const partial=w.created.find(x=>x.name===prefix+next.digest).cache;
  assert.equal(partial.writes,2);assert.equal(partial.responses.has(ready),false);
  assert.equal(w.store.has(prefix+next.digest),false);
  assert.equal((await local.catalog()).find(x=>x.id===w.damaged.id).digest,w.damaged.digest);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
}
{ // A catalog-write failure keeps old publication; its durable new snapshot can retry.
  const w=await seed(), files=pkg('damaged',2), next=(await local.prepare(files)).item;
  const c=w.store.get(cat), before=await snapshot(w.store,cat);
  c.writeError=new DOMException('unavailable','InvalidStateError');
  await assert.rejects(local.install(files),/storage/i);
  assert.deepEqual(await snapshot(w.store,cat),before);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  const stored=w.store.get(prefix+next.digest), writes=stored.writes;
  c.writeError=null;assert.deepEqual(await local.install(files),next);assert.equal(stored.writes,writes);
}
{ // Invalid metadata cannot turn the 64 MiB cap into free space.
  const w=await seed();
  for(let i=0;i<4;i++) {
    const digest=String(i+1).repeat(64),id='local:unknown-'+i;
    const c=await caches.open(prefix+digest);c.responses.set(ready,Response.json({id,size:-1}));
    w.store.get(cat).responses.set(origin+'/local-skin-catalog/'+encodeURIComponent(id),Response.json({local:true,id,digest,size:-1}));
  }
  await assert.rejects(local.install(pkg('newskin')),/64 MiB limit/);
  assert.deepEqual(w.deleted,[]);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
}
{ // A failed exact-cache deletion aborts; it never clears other storage.
  const w = await seed(); w.store.get(prefix + w.damaged.digest).damaged = true;
  caches.deleteError = new DOMException('unavailable', 'InvalidStateError');
  await assert.rejects(local.install(pkg('damaged')), /storage/i);
  assert.deepEqual(w.deleted, [prefix + w.damaged.digest]);
  assert.deepEqual(await snapshot(w.store, prefix + w.healthy.digest), w.before);
}
{ // Corrupt catalog cannot be replaced at the expense of healthy skin records.
  const w = await seed(); w.store.get(cat).damaged = true;
  await assert.rejects(local.install(pkg('newskin')), /storage/i);
  assert.deepEqual(w.deleted, []);
  assert.deepEqual(await snapshot(w.store, prefix + w.healthy.digest), w.before);
}
{ // Orphans/incomplete snapshots are bounded; unrelated cache names stay put.
  const w = await seed();
  const incomplete = prefix + 'a'.repeat(64); await caches.open(incomplete);
  await caches.open('agentnet-skin-package-v1-unrelated'); await caches.open('other-application');
  await local.install(pkg('newskin'));
  assert.deepEqual(w.deleted, [incomplete]);
  assert(w.store.has('agentnet-skin-package-v1-unrelated')); assert(w.store.has('other-application'));
}
// The legacy ready marker cannot reconstruct a healthy skin's catalog entry.
// Catalog repair is permitted only by explicit verified reinstall when every
// unrelated owned snapshot has an unreadable ready marker (UnknownError).
async function unavailableCatalog() {
  const w=await seed();
  for(const name of [cat,prefix+w.healthy.digest,prefix+w.damaged.digest])w.store.get(name).damaged=true;
  return w;
}
async function currentGallery(files) {
  const {item}=await local.prepare(files), published={};
  for(const f of files)published[f.name]=new Uint8Array(await f.arrayBuffer());
  const skin={id:item.package_id,name:item.name,version:'1.0.0',api:1,digest:item.digest,
    files:Object.entries(published).map(([path,bytes])=>({path,size:bytes.length,sha256:sha(bytes)}))};
  return gallery({fetcher:async url=>{
    if(url===LATEST)return Response.json({tag_name:'v1.0.0'});
    if(url===RAW+'v1.0.0/published/catalog.json')return Response.json({catalog:1,skins:[skin]});
    return new Response(published[url.slice((RAW+'v1.0.0/published/'+item.package_id+'/').length)]);
  }});
}
{ // Exact all-unavailable baseline: gallery discovery is read-only; reinstall recovers one digest.
  const w=await unavailableCatalog(), g=await currentGallery(pkg('damaged'));
  const oldCatalog=await snapshot(w.store,cat), writes=[...w.store.values()].map(c=>c.writes);
  assert.equal((await g.list()).skins[0].state,'install');
  assert.deepEqual(w.deleted,[]);assert.deepEqual([...w.store.values()].map(c=>c.writes),writes);
  assert.deepEqual(await snapshot(w.store,cat),oldCatalog);
  assert.equal(await g.install('damaged'),w.damaged.id);
  assert.equal((await g.list()).skins[0].state,'installed');
  assert.deepEqual(w.deleted,[prefix+w.damaged.digest,cat]);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  assert.equal(w.store.get(prefix+w.healthy.digest).damaged,true,'unrelated unavailable bytes retained, not installed');
  assert.deepEqual(await local.catalog(),[w.damaged]);
  const c=w.store.get(prefix+w.damaged.digest), count=c.writes;
  assert.equal(await g.install('damaged'),w.damaged.id);assert.equal(c.writes,count);
  console.log('all-unavailable catalog explicit verified reinstall and duplicate PASS');
}
{ // A new digest has no authority to delete either unreadable old package.
  const w=await unavailableCatalog(), before=await snapshot(w.store,prefix+w.damaged.digest), files=pkg('damaged',2);
  const next=(await local.prepare(files)).item, g=await currentGallery(files);
  assert.equal(await g.install('damaged'),w.damaged.id);assert.notEqual(next.digest,w.damaged.digest);
  assert.deepEqual(w.deleted,[cat]);assert.deepEqual(await local.catalog(),[next]);
  assert.deepEqual(await snapshot(w.store,prefix+w.damaged.digest),before);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
}
{ // A readable unrelated ready marker makes destructive catalog recovery unsound.
  const w=await seed(), before=await snapshot(w.store,cat), chosen=await snapshot(w.store,prefix+w.damaged.digest), g=await currentGallery(pkg('damaged'));
  const writes=[...w.store.values()].map(c=>c.writes), created=w.created.length;
  w.store.get(cat).damaged=true;
  assert.equal((await g.list()).skins[0].state,'install','discovery offers explicit recovery without asserting installation');
  await assert.rejects(local.catalog(),/storage/i);
  await assert.rejects(g.install('damaged'),/storage/i);
  assert.deepEqual(w.deleted,[]);assert.deepEqual(await snapshot(w.store,cat),before);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  assert.deepEqual(await snapshot(w.store,prefix+w.damaged.digest),chosen);
  assert.deepEqual([...w.store.values()].map(c=>c.writes),writes);assert.equal(w.created.length,created);
}
{ // Missing/invalid unrelated ready metadata and generic errors are not permission to reset.
  for(const kind of ['missing','malformed','InvalidStateError','readable-flagged']){
    const w=await unavailableCatalog(), c=w.store.get(prefix+w.healthy.digest);c.damaged=false;
    if(kind==='missing')c.responses.delete(ready);
    else if(kind==='malformed')c.responses.set(ready,new Response('{'));
    else if(kind==='readable-flagged')c.responses.set(ready,Response.json({id:w.healthy.id,size:w.healthy.size,unavailable:true}));
    else c.match=async()=>{throw new DOMException('unavailable',kind);};
    const before=await snapshot(w.store,cat), packages=await Promise.all([w.healthy,w.damaged].map(x=>snapshot(w.store,prefix+x.digest)));
    await assert.rejects(local.install(pkg('damaged')),/storage/i);
    assert.deepEqual(w.deleted,[]);assert.deepEqual(await snapshot(w.store,cat),before);
    assert.deepEqual(await Promise.all([w.healthy,w.damaged].map(x=>snapshot(w.store,prefix+x.digest))),packages);
  }
}
{ // A named catalog open failure cannot stand in for a catalog read failure.
  const w=await unavailableCatalog(), g=await currentGallery(pkg('damaged'));
  caches.openError=new DOMException('unavailable','UnknownError');
  await assert.rejects(g.list(),/storage/i);await assert.rejects(g.install('damaged'),/storage/i);
  assert.deepEqual(w.deleted,[]);
}
{ // Only UnknownError from catalog reads permits discovery's narrow fallback.
  const w=await seed(), g=await currentGallery(pkg('damaged'));
  w.store.get(cat).match=async()=>{throw new DOMException('unavailable','InvalidStateError');};
  await assert.rejects(g.list(),/storage/i);assert.deepEqual(w.deleted,[]);
}
{ // Failed exact catalog deletion preserves it; one explicit retry reuses verified files.
  for(const refusal of ['throw','false']){
  const w=await unavailableCatalog(), before=await snapshot(w.store,cat), original=caches.delete.bind(caches);
  caches.delete=async name=>{if(name===cat){if(refusal==='false')return false;throw new DOMException('unavailable','InvalidStateError');}return original(name);};
  await assert.rejects(local.install(pkg('damaged')),/storage/i);
  assert.deepEqual(await snapshot(w.store,cat),before);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  const c=w.store.get(prefix+w.damaged.digest), writes=c.writes;
  caches.delete=original;assert.deepEqual(await local.install(pkg('damaged')),w.damaged);
  assert.equal(c.writes,writes);assert.deepEqual(w.deleted,[prefix+w.damaged.digest,cat]);
  }
}
{ // Failed new catalog write leaves durable chosen bytes for one explicit retry.
  const w=await unavailableCatalog();caches.failName=cat;caches.writeError=new DOMException('unavailable','InvalidStateError');
  await assert.rejects(local.install(pkg('damaged')),/storage/i);
  const c=w.store.get(prefix+w.damaged.digest), writes=c.writes;
  assert.equal(w.store.get(cat).responses.size,0);assert.deepEqual(await local.catalog(),[]);
  assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
  w.store.get(cat).writeError=null;caches.failName='';
  assert.deepEqual(await local.install(pkg('damaged')),w.damaged);assert.equal(c.writes,writes);
  assert.deepEqual(w.deleted,[prefix+w.damaged.digest,cat]);assert.deepEqual(await local.catalog(),[w.damaged]);
}
{ // Recovery still charges unavailable unrelated packages before deleting anything.
  const w=await unavailableCatalog();
  for(let i=0;i<3;i++)(await caches.open(prefix+String(i+1).repeat(64))).damaged=true;
  await assert.rejects(local.install(pkg('damaged')),/64 MiB limit/);
  assert.deepEqual(w.deleted,[]);assert.deepEqual(await snapshot(w.store,prefix+w.healthy.digest),w.before);
}
{ // Metadata-only chosen repair never deletes intact files on a failed ready write.
  for(const marker of ['missing','malformed']){
    const w=await unavailableCatalog(), c=w.store.get(prefix+w.damaged.digest);c.damaged=false;
    if(marker==='missing')c.responses.delete(ready);else c.responses.set(ready,new Response('{'));
    const before=await snapshot(w.store,prefix+w.damaged.digest), metadata=await snapshot(w.store,cat);
    c.writeError=new DOMException('unavailable','InvalidStateError');
    await assert.rejects(local.install(pkg('damaged')),/storage/i);
    assert.deepEqual(w.deleted,[]);assert.deepEqual(await snapshot(w.store,prefix+w.damaged.digest),before);
    assert.deepEqual(await snapshot(w.store,cat),metadata);
    c.writeError=null;assert.deepEqual(await local.install(pkg('damaged')),w.damaged);
    assert.deepEqual(w.deleted,[cat]);assert.equal(w.store.get(prefix+w.damaged.digest),c);
  }
}
// Claim acknowledgement and controller visibility are separate browser events.
// Exercise their ordering without sleeping or spending the five-second bound.
async function activationCase(options, check) {
  const saved = { navigator, MessageChannel, setTimeout, clearTimeout }, timers = new Map(), channels = [], listeners = new Set();
  let claimListeners;
  const worker = { state:'activated', scriptURL:origin+'/sw.js', postMessage(message) {
    if(message.type==='agentnet-local-skins-activate'){reg.active=worker;reg.waiting=null;worker.state='activated';return;}
    assert.equal(message.type,'agentnet-local-skins-claim');calls.claim++;
    claimListeners=listeners.size;
    if(options.throwClaim)throw Error('claim failed');
    if(options.controlInClaim)control(worker);
    if(options.ackInClaim!==undefined)ack(options.ackInClaim);
  } };
  const old = { state:'activated', scriptURL:worker.scriptURL, postMessage(){assert.fail('the old push-only worker must not receive the claim');} };
  const calls = {register:0,update:0,claim:0};
  const reg = { active:options.waiting?old:options.noActive?null:worker, waiting:options.waiting?worker:null,
    async update(){calls.update++;if(options.offline)throw Error('offline');} };
  if(options.waiting)worker.state='installed';
  const container = { controller:options.oldControl?old:options.controlled?worker:null,
    async register(path){calls.register++;assert.equal(path,'/sw.js');return reg;},
    addEventListener(type,fn){assert.equal(type,'controllerchange');listeners.add(fn);},
    removeEventListener(type,fn){assert.equal(type,'controllerchange');listeners.delete(fn);} };
  const control = next => {container.controller=next;for(const fn of [...listeners])fn();};
  const ack = ready => channels.at(-1).port1.onmessage?.({data:{ready}});
  globalThis.MessageChannel = class {
    constructor(){this.port1={onmessage:null,closed:0,close(){this.closed++;}};this.port2={closed:0,close(){this.closed++;}};channels.push(this);}
  };
  globalThis.setTimeout = (fn,ms) => {const id={};timers.set(id,{fn,ms});return id;};
  globalThis.clearTimeout = id => timers.delete(id);
  Object.defineProperty(globalThis,'navigator',{value:{...navigator,serviceWorker:container},configurable:true});
  let settled=false,error;
  const result=local.activate().then(()=>{settled=true;},e=>{settled=true;error=e;});
  const turn=()=>new Promise(setImmediate);
  const clean=()=>{assert.equal(timers.size,0,'claim timer removed');assert.equal(listeners.size,0,'controller listener removed');if(calls.claim)assert.equal(claimListeners,1,'listen before requesting claim');for(const c of channels){assert.equal(c.port1.closed,1);assert.equal(c.port2.closed,1);assert.equal(c.port1.onmessage,null);}};
  try {
    await turn();
    await check({worker,old,reg,calls,channels,control,ack,turn,result,clean,settled:()=>settled,error:()=>error,
      timeout(){assert.deepEqual([...timers.values()].map(t=>t.ms),[5000],'existing five-second claim/control bound');const [id,timer]=timers.entries().next().value;timers.delete(id);timer.fn();}});
  }finally{Object.defineProperty(globalThis,'navigator',{value:saved.navigator,configurable:true});Object.assign(globalThis,{MessageChannel:saved.MessageChannel,setTimeout:saved.setTimeout,clearTimeout:saved.clearTimeout});}
}
await activationCase({ackInClaim:true},async w=>{
  assert.equal(w.settled(),false,'ready acknowledgement cannot load assets before control');
  w.control(w.old);await w.turn();assert.equal(w.settled(),false,'same script URL is not the selected worker instance');
  w.control(w.worker);await w.result;assert.equal(w.error(),undefined);w.clean();
});
await activationCase({},async w=>{
  w.control(w.worker);await w.turn();assert.equal(w.settled(),false,'control alone does not prove asset-worker readiness');
  w.ack(true);await w.result;assert.equal(w.error(),undefined);w.clean();
});
for(const options of [{controlled:true,ackInClaim:true},{controlInClaim:true,ackInClaim:true},{waiting:true,oldControl:true,ackInClaim:true}])await activationCase(options,async w=>{
  if(options.waiting){assert.equal(w.reg.active,w.worker);assert.equal(w.settled(),false);w.control(w.worker);}
  await w.result;assert.equal(w.error(),undefined);assert.deepEqual(w.calls,{register:1,update:1,claim:1});w.clean();
});
await activationCase({controlled:true,ackInClaim:false},async w=>{await w.result;assert.match(w.error().message,/refused/);w.clean();});
for(const options of [{controlled:true},{ackInClaim:true,oldControl:true}])await activationCase(options,async w=>{
  assert.equal(w.settled(),false);w.timeout();await w.result;assert.match(w.error().message,/Reload AgentNet/);w.clean();
});
await activationCase({throwClaim:true},async w=>{await w.result;assert.match(w.error().message,/claim failed/);w.clean();});
await activationCase({offline:true,controlled:true,ackInClaim:true},async w=>{await w.result;assert.equal(w.error(),undefined);w.clean();});
await activationCase({offline:true,noActive:true},async w=>{await w.result;assert.match(w.error().message,/offline/);assert.equal(w.calls.claim,0);w.clean();});
await activationCase({noActive:true},async w=>{await w.result;assert.match(w.error().message,/unavailable/);assert.equal(w.calls.claim,0);w.clean();});
console.log('local skin activation check PASS: exact controller, both event orders, update/offline/refusal, five-second bound, all resources cleaned');
console.log('local skin storage check PASS: exact-cache repair, verified bytes, rollback, bounded failures, healthy packages, narrow catalog repair');
