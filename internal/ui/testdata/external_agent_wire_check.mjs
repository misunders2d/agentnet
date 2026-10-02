// Source-driven native age/envelope bridge for apx1. No network.
import * as wire from '../static/wire.mjs';
import { Decrypter } from '../static/vendor/age.mjs';
import { createInterface } from 'node:readline';
let keys, address;
async function handle(r) {
  if (r.op === 'setup') { address = r.address; keys = await wire.newKeys(); return { public: wire.marshalPublic(await wire.publicEntry(keys, address)) }; }
  if (r.op === 'excerpt') {
    const h = wire.parseGrantedExcerpt(r.inner, r.info);
    return { json: wire.historyJSON(h), lid: await wire.excerptLID(r.inner.pid, { lid: h.lid, fingerprint: h.from_key }), requirement: wire.agentRequirement(r.inner), history_requirement: wire.agentRequirement({ sub: 'history', body: wire.historyJSON({ ...h, kind: 'message', sub: 'excerpt', pid: r.inner.pid }) }) };
  }
  if (r.op === 'event') { const e = wire.parseEvent(r.json); await wire.verifyEvent(e, wire.unb64(r.key)); return { json: wire.eventJSON(e), hash: await wire.eventHash(e) }; }
  if (r.op === 'open') {
    const n = await wire.open(r.json, keys, address, await wire.parsePublic(JSON.parse(r.from)));
    const d = new Decrypter(); d.addIdentity(keys.box);
    return { inner: n, raw: new TextDecoder().decode(await d.decrypt(wire.parseEnvelope(r.json).ct)) };
  }
  if (r.op === 'seal') return { json: await wire.seal({ ...r.inner, from: address, root: r.inner.root ? JSON.stringify(r.inner.root) : '' }, keys, await wire.parsePublic(JSON.parse(r.to))) };
  if (r.op === 'encrypt-file') {
    const f = await wire.encryptFile(wire.unb64(r.bytes), r.name, await wire.parsePublic(JSON.parse(r.to)));
    return { attachment: f.attachment, ct: wire.b64(f.ct) };
  }
  if (r.op === 'decrypt-file') return { bytes: wire.b64(await wire.decryptFile(wire.unb64(r.ct), r.attachment, keys)) };
  throw new Error('unknown external vector operation');
}
for await (const line of createInterface({ input: process.stdin })) {
  let out; try { out = await handle(JSON.parse(line)); } catch (e) { out = { error: e.message }; }
  process.stdout.write(JSON.stringify(out) + '\n');
}
