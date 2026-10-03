// Needs-you and agent views with an invitation's claimed time (the inviter
// writes it): real Engine methods over synthetic records, no network. A
// claim no Date can hold (9e12 s) or past year 9999 never throws: the
// invitation is listed at the time its first record reached this browser,
// or at a plausible claim when that record is not a row here, else now;
// the agent view leaves an implausible claim out.
import assert from "node:assert/strict";
import { Engine, memoryStore } from "../static/engine.mjs";

const now = Date.UTC(2026, 9, 4, 12, 0, 0);
const e = new Engine({ store: memoryStore(), base: "https://synthetic.invalid", now: () => now,
  fetch: async () => { throw Error("no network in this check"); } });
e.address = "admin/tablet"; e.fp = "f".repeat(8); e.me = { person: "p".repeat(64), label: "Alice", devices: [] };
const conv = "c".repeat(64), pid = "1".repeat(32), received = Date.UTC(2026, 9, 3, 9, 30, 0);
const info = (invited) => ({ pid, role: "", state: "invited", held: 0, grant: [], taskKeys: [], note: "help", invited,
  host: { person: e.me.person, address: "admin/laptop", fingerprint: "a".repeat(8), label: "Alice", devices: [] },
  inviter: { person: "b".repeat(64), address: "bob/desk", fingerprint: "b".repeat(8), label: "Bob", devices: [] } });
const listed = (invited, msgs) => {
  const needs = [], held = [];
  e.needsYouOf(conv, [info(invited)], msgs, [], needs, held);
  assert.equal(needs.length, 1, "the invitation is listed");
  assert.equal(held.length, 0);
  return needs[0];
};
const record = [{ sub: "event", pid, at: received, body: "" }, { sub: "event", pid, at: received + 5000, body: "" }];
for (const claim of [9e12, 1e12, 253402300799, 1759500000, -5, NaN])
  assert.equal(listed(claim, record).at, new Date(received).toISOString(), "listed at its first record's arrival, claim " + claim);
for (const [claim, at] of [[9e12, now], [1e12, now], [253402300799, now], [-5, now], [NaN, now], [0, now], [1759500000, 1759500000e3]])
  assert.equal(listed(claim, []).at, new Date(at).toISOString(), "no record row here: claim " + claim);
for (const [claim, shown] of [[9e12, ""], [1e12, ""], [253402300799, ""], [-5, ""], [0, ""], [1759500000, new Date(1759500000e3).toISOString()]])
  assert.equal(e.agentView(info(claim), [], null).invited, shown, "the agent view's invited, claim " + claim);
console.log("PASS needs-you claimed invitation times: arrival, plausible claim, or now; agent view leaves implausible claims out");
