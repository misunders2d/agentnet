// The browser side of wire_test.go: runs static/wire.mjs with this
// device's real (non-extractable) keys and answers one JSON request per
// line on stdin with one JSON line on stdout. The Go test checks every
// answer with the Go code.
import * as wire from "../static/wire.mjs";
import { createInterface } from "node:readline";

let keys = null;
let address = "";

const bytes = (b) => (b ? wire.b64(b) : "");

async function handle(req) {
  switch (req.op) {
  case "support":
    return { missing: await wire.support() };
  case "setup": {
    address = req.address;
    keys = await wire.newKeys();
    return describe();
  }
  case "reload": // IndexedDB keeps keys by structured cloning: use only the copies from here on
    keys = structuredClone(keys);
    return describe();
  case "goString":
    return { json: req.strings.map((s) => { try { return wire.goString(s); } catch (e) { return "refused: " + e.message; } }) };
  case "join":
    return { body: await wire.joinRequest(keys, address, req.secret) };
  case "request":
    return { headers: await wire.signRequest(keys, address, req.method, req.target, req.body) };
  case "ad":
    return { ad: await wire.sessionAd(keys, address, req.session) };
  case "invite":
    return { invite: wire.decodeInvite(req.code) };
  case "public": {
    const p = await wire.parsePublic(JSON.parse(req.public));
    return { address: p.address, fingerprint: await wire.fingerprint(p) };
  }
  case "seal": {
    const to = await wire.parsePublic(JSON.parse(req.to));
    const m = Object.assign({ from: address }, req.message);
    if (req.body_bytes) m.body = "x".repeat(req.body_bytes);
    return { envelope: await wire.seal(m, keys, to) };
  }
  case "roster": {
    const r = await wire.newRoster(keys, address, req.label);
    return { json: wire.rosterJSON(r), hash: await wire.rosterHash(r) };
  }
  case "parseRoster": {
    const r = wire.parseRoster(req.json);
    await wire.verifyRoster(r, wire.unb64(req.key, "key"));
    return { hash: await wire.rosterHash(r) };
  }
  case "validLabel":
    return { ok: req.labels.map((l) => { try { wire.validLabel(l); return true; } catch (e) { return false; } }) };
  case "root": {
    const c = await wire.newRoot(keys, req.me, req.other);
    return { json: wire.rootJSON(c), id: await wire.rootID(c) };
  }
  case "parseRoot": {
    const c = wire.parseRoot(req.json);
    await wire.verifyRoot(c, wire.unb64(req.key, "key"));
    return { id: await wire.rootID(c) };
  }
  case "caps": {
    const c = await wire.newCaps(keys, address, req.session);
    return { json: wire.capsJSON(c) };
  }
  case "parseCaps": {
    const c = wire.parseCaps(req.json);
    await wire.verifyCaps(c, wire.unb64(req.key, "key"));
    return { ok: true };
  }
  case "safeName":
    return { names: req.names.map((n) => wire.safeName(n)) };
  case "validChannel":
    return { valid: req.list.map((s) => wire.validChannel(s)) };
  case "verifyEnvelope": // an envelope made elsewhere, checked with that sender's signing key
    await wire.verifyEnvelope(wire.parseEnvelope(req.envelope), wire.unb64(req.key, "key"));
    return {};
  case "channel":
    return { chan: await wire.notifyChannel(req.conv, req.fp) };
  case "supports":
    return { supports: await wire.profileSupports(JSON.parse(req.profile), req.address, wire.unb64(req.key, "key"), req.name) };
  case "event": {
    const e = await wire.signEvent(keys, req.event);
    return { json: wire.eventJSON(e), hash: await wire.eventHash(e) };
  }
  case "parseEvent": {
    const e = wire.parseEvent(req.json);
    await wire.verifyEvent(e, wire.unb64(req.key, "key"));
    return { hash: await wire.eventHash(e) };
  }
  case "open": {
    const from = await wire.parsePublic(JSON.parse(req.from));
    return { inner: await wire.open(req.envelope, keys, req.self || address, from) };
  }
  }
  throw new Error("unknown op " + req.op);
}

async function describe() {
  const pub = await wire.publicEntry(keys, address);
  let exportRefused = true;
  for (const k of [keys.sign, keys.box]) {
    try { await crypto.subtle.exportKey("pkcs8", k); exportRefused = false; } catch (e) { /* as intended */ }
  }
  return { public: wire.marshalPublic(pub), fingerprint: await wire.fingerprint(pub),
    extractable: keys.sign.extractable || keys.box.extractable, export_refused: exportRefused, sign_key: bytes(pub.sign_key) };
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
