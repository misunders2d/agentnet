// Comic's held-back status lines (web/src/model.ts, t5-held): one line per
// sending device and cause that names an own verified device as yours,
// counts records instead of envelopes (an estimate, said so, for copies
// held before records were named), and says who can act. A device that
// only claims an address is never named as a person or asked to act.
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
// Older copies without a record: matched by size, and said to be an estimate.
const legacy = flood.slice(0, 600).concat(Array.from({ length: 130 }, (_, i) => copy(1000 + i, { sender_verified: true, action: "update_sender", size: 2840 + 36 * (i % 4) })));
eq(m.heldStatus(legacy, o), "Your device Bezos re-sent about 4 records 730 times", "size-matched legacy rows");
eq(m.heldCount(legacy).estimate, true);
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
