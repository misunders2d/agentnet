// Actual Engine rename/CAS/refresh paths with real keys and memory-only synthetic Hub.
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
const stores = ["kv", "pins", "persons", "convs", "inbox", "outbox", "held", "lids", "receipts", "files"];
let checks = 0;
const check = (v, why) => { assert.ok(v, why); checks++; };
const refuses = async (fn, why) => { await assert.rejects(fn, why); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });
const raw = (r) => JSON.parse(wire.rosterJSON(r));

async function fixture() {
  const store = memoryStore(), chains = new Map(), calls = [], submitted = [];
  let mode = "normal", puts = 0, accepted = 0;
  const e = new Engine({ store, base: "https://synthetic.invalid", now: () => 1790000000123, fetch: async (url, o = {}) => {
    const u = new URL(url); calls.push({ path: u.pathname, after: u.searchParams.get("after"), ...o });
    if (u.pathname.startsWith("/v1/persons/") && u.pathname.endsWith("/chain")) {
      if (mode === "read-offline") throw new Error("synthetic unavailable");
      if (mode === "read-forbidden") return json({ error: "synthetic refused" }, 403);
      const person = u.pathname.split("/")[3], after = Number(u.searchParams.get("after"));
      return json({ records: (chains.get(person) || []).filter((r) => r.seq > after).map(raw), more: false });
    }
    if (u.pathname === "/v1/person" && o.method === "PUT") {
      puts++;
      check(o.headers["X-Agentnet-Agent"] === e.address && !!o.headers["X-Agentnet-Sig"], "existing signed request transport");
      const next = await wire.parseRoster(o.body), records = chains.get(next.person);
      if (["stale-once", "always-stale"].includes(mode) && (mode === "always-stale" || puts === 1)) {
        const previous = records.at(-1);
        records.push(await wire.nextRoster(siblingKeys, siblingAddress, previous, previous.devices, null, "Concurrent " + puts));
      }
      const head = records.at(-1);
      if (next.prev !== await wire.rosterHash(head) || next.seq !== head.seq + 1) return json({ code: "roster_stale", error: "stale head" }, 409);
      await wire.verifyNext(next, head);
      check(next.person === head.person && JSON.stringify(next.devices.map((d) => wire.marshalPublic(d))) === JSON.stringify(head.devices.map((d) => wire.marshalPublic(d))), "rename changes no device or key");
      submitted.push(raw(next));
      if (mode === "lost-unaccepted") throw new Error("synthetic response unavailable before acceptance");
      records.push(next); accepted++;
      if (mode === "lost-accepted") throw new Error("synthetic response lost after acceptance");
      if (mode === "delayed-newer") {
        records.push(await wire.nextRoster(siblingKeys, siblingAddress, next, next.devices, null, "Newer label"));
        await e.refreshPerson(e.me); // new signed proof arrives before the older PUT's response
      }
      if (mode === "changed-after-accept") e.keys = await wire.newKeys();
      return json(null, 204);
    }
    throw new Error("unexpected synthetic request " + u.pathname);
  } });
  e.keys = await wire.newKeys(); e.address = "self/phone";
  const pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(pub);
  const first = await wire.newRoster(e.keys, e.address, "Original label");
  const siblingKeys = await wire.newKeys(), siblingAddress = "self/tablet", siblingPub = await wire.publicEntry(siblingKeys, siblingAddress);
  const join = await wire.joinConsent(siblingKeys, siblingAddress, first.person, 1, await wire.rosterHash(first));
  const linked = await wire.nextRoster(e.keys, e.address, first, [pub, siblingPub], join); await wire.verifyNext(linked, first);
  e.me = { ...(await e.personRecord([first, linked], "self", null)), published: true };
  chains.set(first.person, [first, linked]);
  await store.write([{ s: "kv", k: "identity", v: { address: e.address, keys: e.keys, fingerprint: e.fp } }, { s: "kv", k: "person", v: e.me },
    { s: "kv", k: "role", v: "person" }, { s: "kv", k: "permission-fixture", v: { approved: ["peer/desk"], tasks: { fingerprint: "unchanged" } } },
    { s: "inbox", k: "fixture-question", v: { id: "fixture-question", from: "peer/desk", kind: "question", body: "history", state: "held", at: 1790000000000 } },
    { s: "outbox", k: "fixture-history", v: { id: "fixture-history", body: "sent history", state: "delivered", at: 1790000000000 } }]);
  await e.pinDevices(e.me);
  // Omit only the actual kv/person row, not other identity/role/permission values.
  const sideEffects = async () => JSON.stringify(await Promise.all(stores.map(async (s) => s === "kv"
    ? [s, await Promise.all(["identity", "role", "permission-fixture"].map(async (k) => [k, await store.get(s, k)]))]
    : [s, await store.all(s)])));
  return { e, store, chains, calls, submitted, first, linked, pub, siblingKeys, siblingAddress, siblingPub, sideEffects,
    mode: (value) => { mode = value; }, counts: () => ({ puts, accepted }) };
}

let vector;
// Collision is a display-only change; linked and remote devices follow the same signed head.
{
  const f = await fixture(), { e } = f, before = await f.sideEffects(), key = e.keys, fp = e.fp, address = e.address, person = e.me.person;
  const peerKeys = await wire.newKeys(), peerAddress = "peer/desk", peerRoster = await wire.newRoster(peerKeys, peerAddress, "Duplicate label");
  const peer = await e.personRecord([peerRoster], "pinned", null);
  await f.store.write([{ s: "persons", k: peer.person, v: peer }]);
  const baseline = await f.sideEffects();
  const info = await e.api("/api/person/label", { label: peer.label });
  check(info.label === peer.label && info.person === person && info.person !== peer.person, "identical labels keep distinct immutable IDs");
  check(e.keys === key && e.fp === fp && e.address === address && info.fingerprint === fp && info.address === address && info.state === "self", "keys/address/role untouched");
  check(info.seq === 2 && info.roster === e.me.hash && info.devices[0].added === 0 && info.devices[1].added === 1 && info.devices[0].this === true && !Object.hasOwn(info.devices[1], "this"), "native PersonInfo device/roster fields");
  check(await f.sideEffects() === baseline, "all non-person storage, history, grants and held jobs unchanged");
  check(f.counts().puts === 1 && f.counts().accepted === 1 && (await f.store.get("kv", "person")).label === peer.label, "one CAS accepted before local save");
  const same = await e.api("/api/person/label", { label: peer.label });
  check(f.counts().puts === 1 && same.seq === info.seq, "same label refreshes current proof without another publication");
  const linkedStore = memoryStore(), linked = new Engine({ store: linkedStore, base: e.base, fetch: e.fetch });
  linked.keys = f.siblingKeys; linked.address = f.siblingAddress; linked.fp = await wire.fingerprint(f.siblingPub);
  linked.me = { ...(await linked.personRecord([f.first, f.linked], "self", null)), published: true };
  await linkedStore.write([{ s: "kv", k: "person", v: linked.me }]); await linked.pinDevices(linked.me);
  await linked.refreshPerson(linked.me);
  check(linked.me.label === peer.label && linked.me.person === person && linked.address === f.siblingAddress, "linked device follows signed label without identity change");
  const remoteStore = memoryStore(), remote = new Engine({ store: remoteStore, base: e.base, fetch: e.fetch });
  remote.keys = peerKeys; remote.address = peerAddress; remote.fp = await wire.fingerprint(await wire.publicEntry(peerKeys, peerAddress));
  remote.me = { ...(await remote.personRecord([peerRoster], "self", null)), published: true };
  const pinned = await remote.personRecord([f.first, f.linked], "pinned", null);
  await remoteStore.write([{ s: "persons", k: person, v: pinned }]); await remote.pinDevices(pinned);
  const seen = await remote.refreshPerson(pinned);
  check(seen.label === peer.label && seen.person === person && seen.devices.length === 2, "remote device refreshes exact pinned identity to new label");
  vector = { info, steps: f.chains.get(person).map(raw), other_person: peer.person };
  check(before !== baseline, "explicit peer fixture is separate from rename effects");
  e.stop(); linked.stop(); remote.stop();
}

// Explicit stale CAS is the sole automatic rebase; there are at most two PUTs.
for (const mode of ["stale-once", "always-stale"]) {
  const f = await fixture(), { e } = f, side = await f.sideEffects(); f.mode(mode);
  if (mode === "stale-once") {
    const info = await e.api("/api/person/label", { label: "Desired" });
    check(info.label === "Desired" && info.seq === 3 && f.counts().puts === 2 && f.counts().accepted === 1, "stale CAS re-signs latest head once");
    const records = f.chains.get(e.me.person);
    check(records.at(-1).prev === await wire.rosterHash(records.at(-2)), "rebased signature links to concurrent accepted head");
  } else {
    await refuses(() => e.api("/api/person/label", { label: "Desired" }), /not confirmed/);
    check(f.counts().puts === 2 && !f.counts().accepted && e.me.label !== "Desired", "second explicit stale refuses, no third attempt");
  }
  check(await f.sideEffects() === side, "CAS rebase does not change unrelated durable state"); e.stop();
}

// Unknown network outcome never resends or optimistically adopts proposed label.
for (const mode of ["lost-accepted", "lost-unaccepted"]) {
  const f = await fixture(), { e } = f, before = e.me.hash, side = await f.sideEffects(); f.mode(mode);
  await refuses(() => e.api("/api/person/label", { label: "Maybe accepted" }), /not confirmed/);
  check(f.counts().puts === 1 && e.me.hash === before && (await f.store.get("kv", "person")).hash === before, "ambiguous response leaves local head unchanged");
  f.mode("normal"); await e.refreshPerson(e.me);
  check(mode === "lost-accepted" ? e.me.label === "Maybe accepted" && e.me.seq === 2 : e.me.hash === before, "existing verified refresh recovers actual acceptance only");
  check(f.counts().puts === 1 && await f.sideEffects() === side, "recovery reads proof, never blind-resends or queues rename"); e.stop();
}

// A delayed success cannot roll back a newer pinned head.
{
  const f = await fixture(); f.mode("delayed-newer");
  const info = await f.e.api("/api/person/label", { label: "Older accepted" });
  check(info.label === "Newer label" && info.seq === 3 && f.e.me.label === "Newer label" && (await f.store.get("kv", "person")).seq === 3, "delayed older response retains verified newer head");
  check(f.counts().puts === 1, "newer head read does not republish older requested label"); f.e.stop();
}

// Strict self proof: no unpublished auto-publish, foreign/current signer or fork acceptance.
for (const bad of ["none", "unpublished", "conflict", "changed-key", "wrong-fingerprint", "removed", "fork", "read-offline", "read-forbidden", "no-chain"]) {
  const f = await fixture(), { e } = f;
  if (bad === "none") e.me = null;
  if (bad === "unpublished") e.me.published = false;
  if (bad === "conflict") e.me.state = "conflict";
  if (bad === "changed-key") e.keys = await wire.newKeys();
  if (bad === "wrong-fingerprint") e.fp = await wire.fingerprint(f.siblingPub);
  if (bad === "removed") f.chains.get(e.me.person).push(await wire.nextRoster(f.siblingKeys, f.siblingAddress, f.linked, [f.siblingPub], null));
  if (bad === "fork") f.chains.set(e.me.person, [f.first, await wire.nextRoster(e.keys, e.address, f.first, f.linked.devices, f.linked.join, "Forked")]);
  if (bad.startsWith("read-")) f.mode(bad);
  if (bad === "no-chain") f.chains.set(e.me.person, []);
  const saved = JSON.stringify(await f.store.get("kv", "person"));
  await refuses(() => e.api("/api/person/label", { label: "Refused" }));
  check(f.counts().puts === 0 && JSON.stringify(await f.store.get("kv", "person")) === saved, "invalid self proof makes no PUT/local rename: " + bad); e.stop();
}
{
  const f = await fixture(); f.mode("changed-after-accept");
  await refuses(() => f.e.api("/api/person/label", { label: "Old signing identity" }), /identity changed/);
  check(f.counts().puts === 1 && f.e.me.seq === 1 && (await f.store.get("kv", "person")).seq === 1, "changed signer during accepted response not pinned optimistically"); f.e.stop();
}

// Native label validation: invalid input cannot trim into a new claim or make writes.
{
  const f = await fixture(), saved = JSON.stringify(await f.store.get("kv", "person"));
  for (const value of ["", " bad ", "bad\ncontrol", "x".repeat(65), "Ю".repeat(33), "\ud800", 7, null]) await refuses(() => f.e.api("/api/person/label", { label: value }));
  await refuses(() => f.e.api("/api/person/label", { label: "Fine", person: "foreign" }));
  check(!f.counts().puts && JSON.stringify(await f.store.get("kv", "person")) === saved, "invalid labels/foreign identity input change nothing"); f.e.stop();
}
console.log(JSON.stringify({ checks, ...vector }));
