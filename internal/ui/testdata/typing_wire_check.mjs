// Synthetic real-key bridge for typing_browser_test.go; no transport/storage.
import * as wire from "../static/wire.mjs";
import { createInterface } from "node:readline";
let keys, address;
async function handle(r) {
  if (r.op === "setup") {
    address = r.address; keys = await wire.newKeys();
    return { public: wire.marshalPublic(await wire.publicEntry(keys, address)), extractable: keys.sign.extractable || keys.box.extractable };
  }
  if (r.op === "verify") {
    const s = wire.parseSignal(r.signal);
    await wire.verifySignal(s, wire.unb64(r.key, "key"), r.now);
    return { canonical: new TextDecoder().decode(wire.signalCanonical(s)), hash: wire.hex(await wire.sha256(wire.signalCanonical(s))), json: wire.signalJSON(s) };
  }
  if (r.op === "open") return { plain: await wire.openTyping(r.signal, keys, address, await wire.parsePublic(JSON.parse(r.from)), r.realm, r.now) };
  if (r.op === "seal") return { signal: await wire.sealTyping({ ...r.plain, from: address }, keys, await wire.parsePublic(JSON.parse(r.to))) };
  throw new Error("unknown typing operation");
}
for await (const line of createInterface({ input: process.stdin })) {
  let out; try { out = await handle(JSON.parse(line)); } catch (e) { out = { error: e.message }; }
  process.stdout.write(JSON.stringify(out) + "\n");
}
