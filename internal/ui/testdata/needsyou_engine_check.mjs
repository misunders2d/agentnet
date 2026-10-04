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
// This browser's request to the laptop's agent is listed, read-only, in
// the states client.PageReview lists it in on the laptop: awaiting,
// needs_human, and interrupted (the laptop's daemon stopped while it ran:
// run it again there, or close it there); never while it runs.
const active = { ...info(1759500000), state: "active" };
const req = { id: "2".repeat(32), lid: "3".repeat(32), pid, kind: "task", body: "rotate the key", at: received,
  target: { address: "admin/laptop", fingerprint: "a".repeat(8) } };
const status = (state) => ({ sub: "status", from: "admin/laptop", lid: "4".repeat(32), ref: { id: req.lid, fingerprint: e.fp },
  body: JSON.stringify({ state, n: 1, at: 1759500000 }) });
for (const [state, reason] of [["awaiting", "agent_awaiting"], ["needs_human", "agent_needs_human"], ["interrupted", "agent_interrupted"], ["running", ""]]) {
  const needs = [], held = [];
  e.needsYouOf(conv, [active], [req], [status(state)], needs, held);
  assert.deepEqual(needs.map((n) => n.reason), reason ? [reason] : [], "a request the laptop reports " + state);
  if (reason) assert.equal(needs[0].decide_on, "admin/laptop", "decided on the laptop");
}
// A turn held for the person (conv_held: nothing runs it) is answered by
// the person's own later turn in that conversation, as
// client.turnClosesHeld closes it: one sent from this browser (no key of
// another device) or from another device of theirs (own). A request to an
// agent, an agent's output or an earlier turn answers nothing.
const heldTurn = { id: "5".repeat(32), lid: "6".repeat(32), kind: "question", body: "decide please", at: received, fp: "b".repeat(8), from: "bob/desk", state: "conv_held", read: true };
const turn = (n, extra) => ({ id: String(n).repeat(32), lid: String(n + 1).repeat(32), kind: "message", body: "yes", at: received + 1000, ...extra });
const heldIds = (msgs) => { const needs = [], held = []; e.needsYouOf(conv, [], msgs, [], needs, held); return held.map((h) => h.id); };
assert.deepEqual(heldIds([heldTurn]), [heldTurn.id], "held until answered");
assert.deepEqual(heldIds([heldTurn, turn(7)]), [], "answered from this browser");
assert.deepEqual(heldIds([heldTurn, turn(7, { own: true, fp: "a".repeat(8), ts: received / 1000 + 1 })]), [], "answered from another device of the person");
assert.deepEqual(heldIds([heldTurn, turn(7, { own: true, fp: "a".repeat(8), ts: received / 1000 - 60, at: received + 5000 })]), [heldTurn.id], "a turn written before it arrived, delivered late, answers nothing");
assert.deepEqual(heldIds([{ ...heldTurn, at: received + 400 }, turn(7, { own: true, fp: "a".repeat(8), ts: received / 1000, at: received + 5000 })]), [heldTurn.id], "a turn stamped in the second it arrived may have been written before it: it answers nothing");
assert.deepEqual(heldIds([heldTurn, turn(7, { own: true, fp: "a".repeat(8), origin: "agent:claude", kind: "answer" })]), [heldTurn.id], "an agent's output answers nothing");
assert.deepEqual(heldIds([heldTurn, turn(7, { kind: "question", target: { address: "admin/laptop", fingerprint: "a".repeat(8) } })]), [heldTurn.id], "a request to an agent answers nothing");
assert.deepEqual(heldIds([turn(7, { at: received - 1000 }), heldTurn]), [heldTurn.id], "an earlier turn answers nothing");
console.log("PASS needs-you claimed invitation times: arrival, plausible claim, or now; agent view leaves implausible claims out; requests listed as their host reports them, interrupted included; held turns answered by the person's later turn");
