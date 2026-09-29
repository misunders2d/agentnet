// The browser device's engine for engine_test.go: runs static/engine.mjs in
// node against a real test Hub (trusted by its own certificate through
// NODE_EXTRA_CA_CERTS), with an in-memory store, answering one JSON request
// per line on stdin with one JSON line on stdout.
import { Engine, memoryStore, probeStore, sameOrigin } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
import { createInterface } from "node:readline";

let store = memoryStore();
let engine = null;
let offline = false;
const realFetch = globalThis.fetch.bind(globalThis);
// A network that can be switched off, as a phone on a train.
const fetchImpl = (url, opts) => (offline ? Promise.reject(new TypeError("fetch failed")) : realFetch(url, opts));

async function handle(req) {
  switch (req.op) {
  case "init":
    engine = new Engine({ store, base: req.base, fetch: fetchImpl });
    await engine.load();
    await probeStore(store);
    return { joined: engine.joined };
  case "join":
    return { address: await engine.join(req.code, req.name) };
  case "person":
    return await engine.createPerson(req.label);
  case "start":
    engine.start();
    return {};
  case "status":
    return { connected: engine.connected, revoked: engine.revoked, members: engine.members.current };
  case "api":
    return { v: await engine.api(req.path, req.body) };
  case "offline":
    offline = req.on;
    if (offline && engine.abort) engine.abort.abort();
    if (!offline) engine.kick();
    return {};
  case "reload": { // the page is reloaded: a new engine over the same stored data
    engine.stop();
    engine = new Engine({ store, base: req.base, fetch: fetchImpl });
    await engine.load();
    engine.start();
    return { joined: engine.joined };
  }
  case "tamperPin": { // the pin of address now names another key (as if it changed)
    const pin = await store.get("pins", req.address);
    await store.write([{ s: "pins", k: req.address, v: { ...pin, json: req.json, fingerprint: req.fingerprint } }]);
    engine.pubs.clear();
    return {};
  }
  case "sameOrigin":
    sameOrigin(req.hub, req.base);
    return {};
  case "count":
    return { n: (await store.all(req.store)).length };
  case "keys": {
    const id = await store.get("kv", "identity");
    let exported = true;
    try { await crypto.subtle.exportKey("pkcs8", id.keys.box); } catch (e) { exported = false; }
    return { extractable: id.keys.sign.extractable || id.keys.box.extractable, exported, fingerprint: id.fingerprint,
      public: wire.marshalPublic(await wire.publicEntry(id.keys, id.address)) };
  }
  }
  throw new Error("unknown op " + req.op);
}

const rl = createInterface({ input: process.stdin });
for await (const line of rl) {
  let out;
  try {
    out = await handle(JSON.parse(line));
  } catch (e) {
    out = { error: e.message };
  }
  process.stdout.write(JSON.stringify(out) + "\n");
}
process.exit(0);
