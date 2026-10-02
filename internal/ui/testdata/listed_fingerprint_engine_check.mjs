// Narrow listed-contact projection regression; real roster/key validation,
// memory-only directory responses, no pinning or persistent writes.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { Engine, memoryStore } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";

const stores = ["kv", "pins", "persons", "convs", "inbox", "outbox", "held", "lids", "receipts", "files"];
const store = memoryStore(), profiles = new Map(), calls = [];
const profileReads = () => calls.filter(p => p.endsWith("/profile")).length;
const e = new Engine({ store, base: "https://synthetic.invalid", fetch: async (url, opts) => {
  const p = new URL(url).pathname; calls.push(p);
  assert.equal(opts.method, "GET");
  assert(opts.headers["X-Agentnet-Sig"], "directory read keeps existing signed transport");
  assert(profiles.has(p), "only existing profile endpoint is requested");
  return new Response(JSON.stringify({ person: profiles.get(p) }), { status: 200 });
} });
e.keys = await wire.newKeys(); e.address = "self/phone";
e.fp = await wire.fingerprint(await wire.publicEntry(e.keys, e.address));
const aKeys = await wire.newKeys(), aAddress = "alice/laptop";
const first = await wire.newRoster(aKeys, aAddress, "Same name");
const bKeys = await wire.newKeys(), bAddress = "alice/tablet", bPublic = await wire.publicEntry(bKeys, bAddress);
const join = await wire.joinConsent(bKeys, bAddress, first.person, 1, await wire.rosterHash(first));
const two = await wire.nextRoster(aKeys, aAddress, first, [first.devices[0], bPublic], join);
await wire.verifyNext(two, first);
const other = await wire.newRoster(await wire.newKeys(), "bob/desk", "Same name");
const pinnedRoster = await wire.newRoster(await wire.newKeys(), "pinned/desk", "Same name");
const pinned = await e.personRecord([pinnedRoster], "pinned", null);
await store.write([{ s: "persons", k: pinned.person, v: pinned },
  { s: "kv", k: "permission-fixture", v: { approved: ["already/allowed"], tasks: false } }]);
const snapshot = () => Promise.all(stores.map(async s => [s, await store.all(s)]));
const baseline = await snapshot();
let writes = 0;
const write = store.write.bind(store);
store.write = async ops => { writes++; await write(ops); };
const member = async (r, address = r.devices[0].address) => ({ address, person: { id: r.person, seq: r.seq, hash: await wire.rosterHash(r) } });
for (const [address, r] of [[aAddress, two], [bAddress, two], ["bob/desk", other]])
  profiles.set("/v1/agents/" + address + "/profile", JSON.parse(wire.rosterJSON(r)));
e.members.list = [await member(two), await member(two, bAddress), await member(other), await member(pinnedRoster)];
await e.fillListed();
assert.equal(profileReads(), 2, "one directory read per person step, pinned person skipped");
let overview = await e.overview();
const listed = overview.people.filter(p => p.state === "listed");
assert.equal(listed.length, 2, "duplicate names remain two distinct listed contacts");
const a = listed.find(p => p.address === aAddress);
assert(!Object.hasOwn(a, "person"), "listed contact remains address-keyed, without verified person ID");
assert.deepEqual(a.devices.map(d => d.address), [aAddress, bAddress], "roster device ordering retained");
const expected = two.devices.map(d => {
  const h = createHash("sha256").update(d.sign_key).update(d.box_recipient, "utf8").digest("hex").slice(0, 32);
  return h.match(/.{8}/g).join("-");
});
assert.notEqual(expected[0], expected[1]);
assert.deepEqual(a.devices.map(d => d.fingerprint), expected, "known public keys expose their standard fingerprints");
assert.deepEqual(overview.people.find(p => p.person === pinned.person), e.personView(pinned), "pinned projection unchanged");
assert.equal(writes, 0); assert.deepEqual(await snapshot(), baseline, "no pin/person/DM/permission writes");
await e.fillListed();
assert.equal(profileReads(), 2, "same roster steps remain cached");

// Invalid device binding, a different claimed person and a hash mismatch all
// remain undisplayed; no alternate key lookup or trust transition is added.
const badPublic = JSON.parse(wire.rosterJSON(other)); badPublic.devices[0].sign_key = Buffer.alloc(32).toString("base64");
profiles.set("/v1/agents/invalid/desk/profile", badPublic);
profiles.set("/v1/agents/wrongperson/desk/profile", JSON.parse(wire.rosterJSON(other)));
profiles.set("/v1/agents/wronghash/desk/profile", JSON.parse(wire.rosterJSON(other)));
e.members.list = [
  { address: "invalid/desk", person: { id: other.person, hash: await wire.rosterHash(other) } },
  { address: "wrongperson/desk", person: { id: first.person, hash: await wire.rosterHash(other) } },
  { address: "wronghash/desk", person: { id: other.person, hash: "0".repeat(64) } },
];
// Remove successful cached steps so each supplied profile is actually parsed.
e.listed.clear();
await e.fillListed();
assert.equal(e.listed.size, 0);
overview = await e.overview();
assert.equal(overview.people.filter(p => p.state === "listed").length, 0, "invalid/mismatched roster is not shown");
assert.equal(writes, 0); assert.deepEqual(await snapshot(), baseline);

// Preserve the existing per-step read ceiling and removal of obsolete entries.
const before = profileReads();
e.members.list = [];
for (let i = 0; i < 65; i++) {
  const address = "bounded/device" + i;
  profiles.set("/v1/agents/" + address + "/profile", {});
  e.members.list.push({ address, person: { id: other.person, hash: i.toString(16).padStart(64, "0") } });
}
await e.fillListed(); assert.equal(profileReads() - before, 64);
e.listed.set("f".repeat(64), { person: two.person, devices: [] });
e.members.list = []; await e.fillListed(); assert.equal(e.listed.size, 0);
assert.equal(writes, 0); assert.deepEqual(await snapshot(), baseline);
e.stop();
console.log("PASS listed fingerprints: two devices, distinct duplicate contacts, unchanged pinned state/storage, rejected invalid/person/hash mismatch, cache/read bound");
