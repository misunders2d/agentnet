// ROOM_V1 §2.2 and §8 in the browser engine, as the core resolves rooms
// (client participation.go): real Engine methods over synthetic records,
// no network. A room invitation and its exact scope count; the
// participation carries its audience and end time; scopes standing for an
// invitation not held must agree on audience, end time and group binding;
// with the invitation held only its exact projection counts; in a group a
// room scope counts once its binding verifies (dmMembers' groupInvites),
// never for an invitation held here;
// past its end time, by this device's clock, it counts as ended.
import assert from "node:assert/strict";
import * as wire from "../static/wire.mjs";
import { Engine, memoryStore } from "../static/engine.mjs";

const now = Date.UTC(2026, 9, 4, 12, 0, 0), sec = Math.floor(now / 1000);
const e = new Engine({ store: memoryStore(), base: "https://synthetic.invalid", now: () => now, fetch: async () => { throw Error("no network in this check"); } });
const id = (c) => c.repeat(32), h64 = (c) => c.repeat(64), fp = (c) => [c, c, c, c].map((x) => x.repeat(8)).join("-");
const alice = { person: id("a"), hashes: [h64("a")], devices: [{ address: "alice/desk", fingerprint: fp("a") }] };
const bob = { person: id("b"), hashes: [h64("b")], devices: [{ address: "bob/laptop", fingerprint: fp("b") }] };
const members = (group) => { const m = new Map([[alice.person, alice], [bob.person, bob]]); m.hosts = new Map(); if (group) { m.group = {}; m.epochs = new Map([[fp("a"), h64("1")], [fp("b"), h64("2")]]); m.groupInvites = new Set(); } return m; };
const conv = h64("c");
const author = (group) => ({ person: alice.person, roster: h64("a"), address: "alice/desk", fingerprint: fp("a"), ...(group ? { group_admission: h64("1") } : {}) });
const invite = (fields = {}) => ({ v: 1, conv, pid: fields.pid || wire.newID(), type: "invite", prev: "", author: author(!!fields.group), ts: sec - 60,
  host: { person: bob.person, address: "bob/laptop", fingerprint: fp("b") }, grant: null, audience: "room", task_keys: null, note: "", ...fields });
const rec = async (ev) => ({ e: ev, hash: await wire.eventHash(ev) });
const accept = async (inv) => ({ v: 1, conv, pid: inv.pid, type: "accept", prev: await wire.eventHash(inv), ts: sec - 30, author: { person: bob.person, roster: h64("b"), address: "bob/laptop", fingerprint: fp("b"), ...(inv.group ? { group_admission: h64("2") } : {}) },
  host: null, grant: null, audience: "", task_keys: null, note: "" });
for (const ev of [invite(), invite({ until: sec + 3600 })]) wire.validateEvent(ev);

// The invitation, its exact scope and the host's accept: active, a room participant.
const inv = invite({ until: sec + 3600 }), scope = await wire.scopeOf(inv, sec - 50), acc = await accept(inv);
let info = e.resolveAgent(inv.pid, [await rec(inv), await rec(scope), await rec(acc)], members(false));
assert.equal(info.state, "active");
assert.equal(info.audience, "room");
assert.equal(info.until, sec + 3600);
assert.equal(info.scope, await wire.eventHash(scope), "the exact projection counts");
// With the invitation held, a scope that disagrees on the end time is not its scope.
const other = { ...scope, until: sec + 7200 };
info = e.resolveAgent(inv.pid, [await rec(inv), await rec(other), await rec(acc)], members(false));
assert.equal(info.state, "active");
assert.equal(info.scope, "", "a scope with another end time is not the invitation's projection");

// Scopes standing for an invitation not held: equal ones count; ones that
// disagree on audience or end time are a conflict.
const lone = invite({ until: sec + 3600 });
info = e.resolveAgent(lone.pid, [await rec(await wire.scopeOf(lone, sec - 50)), await rec(await wire.scopeOf(lone, sec - 40))], members(false));
assert.equal(info.state, "invited");
assert.equal(info.audience, "room");
assert.equal(info.until, sec + 3600);
for (const change of [{ until: sec + 1 }, { audience: "conversation", until: 0 }]) {
  const x = { ...await wire.scopeOf(lone, sec - 40), ...change };
  info = e.resolveAgent(lone.pid, [await rec(await wire.scopeOf(lone, sec - 50)), await rec(x)], members(false));
  assert.equal(info.state, "conflict", "scopes disagreeing on " + Object.keys(change).join(","));
}

// Past its end time it counts as ended here.
const past = invite({ until: sec - 1 });
info = e.resolveAgent(past.pid, [await rec(past), await rec(await accept(past))], members(false));
assert.equal(info.state, "dismissed");
assert.equal(info.dismissal, "");

// In a group: a room scope with its invitation's binding counts once that
// binding verifies (dmMembers lists its hash), and not otherwise.
const ginv = invite({ group: { seq: 3, hash: h64("e"), host_role: "member", host_admission: h64("2"), task_admissions: null } });
wire.validateEvent(ginv);
const gscope = await wire.scopeOf(ginv, sec - 50), gacc = await accept(ginv);
wire.validateEvent(gscope);
const m = members(true);
info = e.resolveAgent(ginv.pid, [await rec(gscope), await rec(gacc)], m);
assert.equal(info.invite, "", "an unverified group binding does not count");
assert.ok(info.held > 0);
m.groupInvites.add(await wire.eventHash(gscope));
info = e.resolveAgent(ginv.pid, [await rec(gscope), await rec(gacc)], m);
assert.equal(info.state, "active");
assert.equal(info.scope, await wire.eventHash(gscope));
assert.equal(info.external, false);
// The invitation itself held here (its binding does not verify): its
// verified scope does not stand in for it (client resolve).
info = e.resolveAgent(ginv.pid, [await rec(ginv), await rec(gscope), await rec(gacc)], m);
assert.equal(info.invite, "", "a scope does not stand in for an invitation held here");
assert.notEqual(info.state, "active");
assert.ok(info.held > 0);
console.log("PASS room engine: room audience and end time resolved, exact projection only, agreeing scopes, end time by this clock, group scope counted once its binding verifies, never for an invitation held here");
