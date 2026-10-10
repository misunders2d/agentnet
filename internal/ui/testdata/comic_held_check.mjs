// Comic's held-back status lines (web/src/model.ts, t5-held): one line per
// sending device and cause that names an own verified device as yours,
// counts records instead of envelopes (an estimate, said so, for copies
// held before records were named), and says who can act. A device that
// only claims an address is never named as a person or asked to act.
// "Re-sent" is said only of copies known to repeat a record: background
// copies with a logical record, or sized like one in the same group. Equal
// sizes alone prove nothing, and trust decisions keep a neutral count.
import assert from "node:assert/strict";
import * as m from "../web/src/model.ts";

const me = { person: "p-me", label: "Sergey", address: "admin/zenbook", state: "self", devices: [{ address: "admin/zenbook" }, { address: "admin/bezos" }] };
const bohdan = { person: "p-b", label: "Bohdan", address: "bohdan/windows-laptop", state: "pinned", devices: [{ address: "bohdan/windows-laptop" }] };
const o = { me: { address: "admin/zenbook" }, person: me, people: [bohdan] };
const copy = (i, q) => ({ id: String(i).padStart(32, "0"), peer: "admin/bezos", code: "invalid", detail_code: "participation_binding_mismatch", reason: "", detail: "", recovery: "", at: "2026-10-10T06:40:55Z", can_archive: true, ...q });
let checks = 0;
const eq = (got, want, why) => { assert.equal(got, want, why); checks++; };

// 866 copies of 4 records from an own device that opened under its key.
const flood = Array.from({ length: 866 }, (_, i) => copy(i, { sender_verified: true, action: "update_sender", logical: "abcd".repeat(7) + String(i % 4).repeat(4), size: 2840 + 36 * (i % 4) }));
eq(m.heldStatus(flood, o), "Your device Bezos re-sent 4 records 866 times", "own flood");
eq(m.heldAction(flood[0], o), "update AgentNet on Bezos", "own device action");
eq(m.holdVerified(flood[0]), true, "opened under its key: not unverified");
// The daemon folds exact re-sent copies into one notice (client heldcopy.go):
// 4 notices standing for 866 copies are the same 4 records re-sent, and the
// group's last arrival is its newest folded copy.
const folded = flood.slice(0, 4).map((q, i) => ({ ...q, copies: i < 2 ? 215 : 216, last_at: i === 2 ? "2026-10-10T09:00:00+03:00" : "2026-10-10T06:41:00Z" }));
eq(m.heldStatus(folded, o), "Your device Bezos re-sent 4 records 866 times", "folded flood");
eq(m.heldCopies(folded), 866, "folded copies counted");
eq(m.heldLast(folded), "2026-10-10T06:41:00Z", "newest folded arrival as an instant");
eq(m.heldStatus(folded.slice(0, 1), o), "Your device Bezos re-sent 1 record 216 times", "one record and its copies");
// A folded notice held before records were named still says only copies.
eq(m.heldStatus([copy(40, { sender_verified: true, size: 2840, copies: 9 })], o), "Your device Bezos · 10 held copies, 1 envelope size", "folded legacy notice");
// Older copies without a record: matched by size, and said to be an estimate.
const legacy = flood.slice(0, 600).concat(Array.from({ length: 130 }, (_, i) => copy(1000 + i, { sender_verified: true, action: "update_sender", size: 2840 + 36 * (i % 4) })));
eq(m.heldStatus(legacy, o), "Your device Bezos re-sent about 4 records 730 times", "size-matched legacy rows");
eq(m.heldCount(legacy).estimate, true);
// Copies with no record and no size like one: copies and sizes, no records.
const sized = Array.from({ length: 866 }, (_, i) => copy(i, { sender_verified: true, action: "update_sender", size: 2840 + 36 * (i % 4) }));
eq(m.heldStatus(sized, o), "Your device Bezos · 866 held copies, 4 envelope sizes", "size alone is no record");
eq(m.heldStatus(sized.slice(0, 2).concat(flood.slice(0, 1)), o), "Your device Bezos · 3 held copies, 2 envelope sizes", "a record does not vouch for other sizes");
// Copies held before they opened never carry a record: equal sizes are not one record re-sent.
const claimed = Array.from({ length: 3 }, (_, i) => copy(10 + i, { detail_code: "", size: 2000 }));
eq(m.heldStatus(claimed, o), "A device claiming to be admin/bezos · 3 held copies, 1 envelope size", "unverified background copies");
// Trust decisions: different messages from a changed key, same length, never opened.
const changed = Array.from({ length: 5 }, (_, i) => copy(20 + i, { peer: "bohdan/windows-laptop", code: "key_changed", detail_code: "", can_archive: false, size: 1200 }));
eq(m.heldStatus(changed, o), "5 held messages · Bohdan’s Windows laptop", "key_changed group is a neutral count");
eq(m.heldStatus(changed.map(q => ({ ...q, code: "identity_conflict", logical: "e".repeat(32) })), o), "5 held messages · Bohdan’s Windows laptop", "decisions never say re-sent");
eq(m.heldStatus(Array.from({ length: 2 }, (_, i) => copy(30 + i, { code: "unverified", detail_code: "", can_archive: false, size: 700 })), o), "2 held messages · A device claiming to be admin/bezos", "unverified decision");
// A provider that gives neither: copies only, no invented record count.
eq(m.heldStatus([copy(1, { sender_verified: true }), copy(2, { sender_verified: true })], o), "Your device Bezos · 2 held copies", "unknown records");
// Exactly as many records as copies: no "re-sent".
eq(m.heldStatus([copy(1, { sender_verified: true, logical: "a".repeat(32), size: 1 }), copy(2, { sender_verified: true, logical: "b".repeat(32), size: 1 })], o), "Your device Bezos sent 2 records", "distinct records");
// Another person's device: ask them.
const theirs = copy(3, { peer: "bohdan/windows-laptop", sender_verified: true, action: "update_sender", logical: "c".repeat(32), size: 9 });
eq(m.heldSender(theirs, o), "Bohdan’s Windows laptop");
eq(m.heldAction(theirs, o), "ask Bohdan to update AgentNet on Windows laptop");
// A claim proves nothing: no name, no action, even with an action code.
const claim = copy(4, { peer: "admin/bezos", detail_code: "", action: "update_sender" });
eq(m.holdVerified(claim), false);
eq(m.heldSender(claim, o), "A device claiming to be admin/bezos");
eq(m.heldAction(claim, o), "", "a claimed sender is asked nothing");
// Waits say what they wait for; legacy proof-pending items stay verified.
eq(m.heldAction(copy(5, { code: "proof_pending", action: "wait_invitation" }), o), "waiting for its invitation");
eq(m.heldAction(copy(6, { code: "proof_pending", action: "wait_context" }), o), "waiting for context");
eq(m.holdVerified({ code: "proof_pending" }), true, "older provider without sender_verified");
eq(m.holdVerified({ code: "key_changed" }), true);
eq(m.holdVerified({ code: "invalid" }), false);
console.log(JSON.stringify({ ok: true, checks }));
