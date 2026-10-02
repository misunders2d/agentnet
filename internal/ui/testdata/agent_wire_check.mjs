// Synthetic real-key named-agent bridge; no transport or local executor.
import * as wire from "../static/wire.mjs";
import { Decrypter } from "../static/vendor/age.mjs";
import { createInterface } from "node:readline";
let keys, address;
async function handle(r) {
  if (r.op === "setup") {
    address = r.address; keys = await wire.newKeys();
    return { public: wire.marshalPublic(await wire.publicEntry(keys, address)), extractable: keys.sign.extractable || keys.box.extractable };
  }
  if (r.op === "agent") {
    const a = wire.parseAgentRecord(r.json);
    await wire.verifyAgent(a, await wire.parsePublic(JSON.parse(r.host)));
    return { json: wire.agentJSON(a), canonical: new TextDecoder().decode(wire.agentCanonical(a)), hash: await wire.agentHash(a) };
  }
  if (r.op === "sign-agent") {
    const p = await wire.publicEntry(keys, address);
    const a = await wire.signAgent(keys, { ...r.fields, host: address, host_key: await wire.fingerprint(p) });
    return { json: wire.agentJSON(a) };
  }
  if (r.op === "event") {
    const e = wire.parseEvent(r.json); await wire.verifyEvent(e, wire.unb64(r.key, "key"));
    return { json: wire.eventJSON(e), hash: await wire.eventHash(e) };
  }
  if (r.op === "sign-event") {
    const p = await wire.publicEntry(keys, address);
    const e = await wire.signEvent(keys, { ...r.fields, author: { ...r.fields.author, address, fingerprint: await wire.fingerprint(p) } });
    return { json: wire.eventJSON(e) };
  }
  if (r.op === "history") {
    const h = wire.parseHistory(r.json);
    return { json: wire.historyJSON(h), requirement: wire.agentRequirement({ sub: "history", body: r.json }) };
  }
  if (r.op === "open") {
    const n = await wire.open(r.json, keys, address, await wire.parsePublic(JSON.parse(r.from)));
    const d = new Decrypter(); d.addIdentity(keys.box);
    return { inner: n, raw: new TextDecoder().decode(await d.decrypt(wire.parseEnvelope(r.json).ct)) };
  }
  if (r.op === "seal") return { json: await wire.seal({ ...r.inner, from: address, root: r.inner.root ? JSON.stringify(r.inner.root) : "" }, keys, await wire.parsePublic(JSON.parse(r.to))) };
  throw new Error("unknown named-agent operation");
}
for await (const line of createInterface({ input: process.stdin })) {
  let out; try { out = await handle(JSON.parse(line)); } catch (e) { out = { error: e.message }; }
  process.stdout.write(JSON.stringify(out) + "\n");
}
