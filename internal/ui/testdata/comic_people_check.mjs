// Comic's people model (MEL-529, MEL-525): a device is an agent only when it
// says it runs one; own devices are you; other people's devices are those
// people; look-alike names get a short key. model.ts holds every decision
// as a pure helper (type-only imports), so Node can import it directly.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import * as m from "../web/src/model.ts";

for (const v of JSON.parse(await readFile(new URL("./device_words.json", import.meta.url), "utf8")))
  assert.equal(m.niceDevice(v.in), v.out, "niceDevice(" + JSON.stringify(v.in) + ")");

const me = { person: "p-me", label: "Sergey", address: "admin/laptop", state: "self", fingerprint: "11111111-a",
  devices: [{ address: "admin/laptop", name: "laptop", fingerprint: "11111111-a", this: true },
    { address: "admin/pixel", name: "pixel", fingerprint: "19c77bce-b" }, { address: "admin/zenbook", name: "zenbook", fingerprint: "22222222-c" }] };
const vitalii = { person: "p-vit", label: "Vitalii", address: "vitalii/desk", state: "pinned",
  devices: [{ address: "vitalii/desk", name: "desk", fingerprint: "33333333-d" }, { address: "vitalii/phone", name: "phone", fingerprint: "44444444-e" }] };
const bohdan = { label: "Bohdan", address: "bohdan/windows-laptop", state: "listed",
  devices: [{ address: "bohdan/windows-laptop", name: "windows-laptop", fingerprint: "55555555-f" }, { address: "bohdan/laptop-browser", name: "laptop-browser", fingerprint: "66666666-g" }] };
const fake = { person: "p-fake", label: "sergey", address: "mallory/box", state: "pinned", devices: [{ address: "mallory/box", name: "box", fingerprint: "77777777-h" }] };
const laptop = (agent) => ({ me: { address: "admin/laptop", fingerprint: "11111111-a", responder: agent ? "claude" : "", responder_dir: "", agent },
  person: me, people: [vitalii, bohdan, fake], agent_devices: ["admin/zenbook", "vitalii/desk", "bohdan/windows-laptop"], threads: [], dms: [] });
const o = laptop(true);

// runsAgent: only what the device says; a phone never.
assert.equal(m.runsAgent("admin/pixel", o), false);
assert.equal(m.runsAgent("admin/zenbook", o), true);
assert.equal(m.runsAgent("admin/laptop", o), true, "this device: me.agent");
assert.equal(m.runsAgent("admin/laptop", laptop(false)), false);
assert.deepEqual([...m.agentPeers(o)].sort(), ["admin/zenbook", "bohdan/windows-laptop", "vitalii/desk"]);
assert.equal(m.runsAgent("x/y", { ...o, agent_devices: null }), false, "a null list is no agent");

// Chat rows: the own phone is never "Your agent · on Pixel".
let r = m.threadRow("admin/pixel", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle, r.local], ["agent", "Your agent", "On this computer · asked from your Pixel", true]);
r = m.threadRow("admin/pixel", laptop(false), {});
assert.deepEqual([r.kind, r.title], ["person", "Your Pixel"], "a computer running no agent: the phone is you");
r = m.threadRow("admin/zenbook", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Your agent", "on Zenbook"]);
r = m.threadRow("admin/zenbook", { ...laptop(false), agent_devices: [] }, {});
assert.deepEqual([r.kind, r.title], ["person", "Your Zenbook"], "its responder removed: your Zenbook (seen from a device running none)");
r = m.threadRow("admin/zenbook", { ...o, agent_devices: [] }, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Your agent", "On this computer · asked from your Zenbook"], "seen from a computer running an agent: its questions come here");
r = m.threadRow("vitalii/phone", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["person", "Vitalii", "from Phone"]);
r = m.threadRow("vitalii/desk", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Vitalii’s agent", "on Desk"]);
r = m.threadRow("hub/bezos", { ...o, agent_devices: ["hub/bezos"] }, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Bezos", "Agent · not linked to a person"]);
const list = m.chatList({ ...o, threads: [{ id: "t1", peer: "admin/pixel", title: "x", last: "hi", last_at: "2026-10-05T10:00:00Z", count: 1, review: 0, unread: 0, running: 0, waiting: false, key_changed: false, notices: 0 }] }, {});
assert.equal(list[0].title, "Your agent"); assert.notEqual(list[0].subtitle, "on Pixel");

// Message authors in the laptop↔Pixel thread.
const q = { id: "q1", dir: "in", from: "admin/pixel", kind: "question", body: "?", state: "answered" };
const a = { id: "a1", dir: "out", from: "admin/laptop", kind: "answer", reply_to: "q1", body: "!" };
const typed = { id: "m1", dir: "in", from: "admin/pixel", kind: "message", body: "thanks" };
let w = m.threadAuthor(q, o, {}, "admin/pixel", [q, a]);
assert.deepEqual([w.name, w.sub, w.mine, w.agent], ["You", "from Pixel", true, false], "a question typed on the phone is yours");
w = m.threadAuthor(typed, o, {}, "admin/pixel", [q, a, typed]);
assert.deepEqual([w.name, w.mine], ["You", true], "plain messages too");
w = m.threadAuthor(a, o, {}, "admin/pixel", [q, a]);
assert.deepEqual([w.name, w.agent, w.mine], ["Your agent", true, false], "only the laptop agent's answers are on the left");
w = m.threadAuthor(a, o, {}, "admin/pixel", [{ ...q, state: "manual" }, a]);
assert.deepEqual([w.name, w.mine], ["You", true], "answered by hand: you");
w = m.threadAuthor({ id: "v", dir: "in", from: "vitalii/phone", kind: "message", body: "hi" }, o, {}, "vitalii/phone");
assert.deepEqual([w.name, w.sub, w.mine, w.agent], ["Vitalii", "from Phone", false, false]);
w = m.threadAuthor({ id: "z", dir: "in", from: "admin/zenbook", kind: "answer", body: "ok" }, o, {}, "admin/zenbook");
assert.deepEqual([w.name, w.agent], ["Your agent", true]);
w = m.threadAuthor({ id: "o", dir: "out", from: "admin/laptop", kind: "question", body: "?" }, o, {}, "admin/zenbook");
assert.deepEqual([w.name, w.mine], ["You", true]);

// The composer: no Ask toward a device that runs no agent.
assert.deepEqual(m.deviceTarget("admin/zenbook", o), { kind: "agent" });
assert.equal(m.deviceTarget("admin/pixel", o).kind, "own");
assert.match(m.deviceTarget("admin/pixel", o).note, /your own Pixel\. Questions you ask there come to this computer’s agent\./);
assert.deepEqual(m.deviceTarget("vitalii/phone", o), { kind: "person", name: "Vitalii" });

// Bring in: Bohdan's browser is never offered; his agent device is, while it publishes no named agents.
assert.deepEqual(m.bringInDevices(bohdan, o, {}), ["bohdan/windows-laptop"]);
assert.deepEqual(m.bringInDevices(bohdan, o, { "bohdan/windows-laptop": [{ id: "x" }] }), [], "named agents are offered one by one");
assert.deepEqual(m.bringInDevices(vitalii, o, {}), ["vitalii/desk"]);

// Look-alike names: a short key; the real owner's own devices have none.
let d = m.deviceWho("mallory/box", o);
assert.equal(m.whoName(d), "sergey · 77777777", "a name equal to yours gets the key");
d = m.deviceWho("bohdan/laptop-browser", o);
assert.equal(d.verified, false); assert.equal(m.whoName(d), "Bohdan · 66666666", "a listed name gets the key");
d = m.deviceWho("admin/pixel", o);
assert.deepEqual([d.relation, d.short, d.name], ["own", "", "You"]);
d = m.deviceWho("vitalii/phone", o);
assert.equal(m.whoName(d), "Vitalii");

// Who asked, for OKs and the needs-you banner.
assert.deepEqual(m.askerWords("admin/pixel", o, {}), { name: "you", device: "Pixel", agent: false, you: true });
assert.deepEqual(m.askerWords("vitalii/phone", o, {}), { name: "Vitalii", device: "Phone", agent: false, you: false });
assert.equal(m.askerWords("vitalii/desk", o, {}).agent, true);

// A phone (a browser runs no agent): the thread with the laptop is your agent there.
const phone = { ...laptop(false), me: { address: "admin/pixel", fingerprint: "19c77bce-b", responder: "", responder_dir: "", agent: false }, agent_devices: ["admin/laptop"] };
r = m.threadRow("admin/laptop", phone, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Your agent", "on Laptop"]);
w = m.threadAuthor({ id: "o", dir: "out", from: "admin/pixel", kind: "question", body: "?" }, phone, {}, "admin/laptop");
assert.equal(w.name, "You");
console.log("Comic people model: agents only where one runs, own devices are you, people are people, look-alikes keyed, Bring in agents only PASS");

// Same-named assistants remain distinguishable in compact labels and mentions.
const names = { mine: "Codex", theirs: "Codex" };
assert.equal(m.agentName("mine", names, me, me), "Your Codex");
assert.equal(m.agentName("theirs", names, vitalii, me), "Vitalii’s Codex");
const agents = [{ pid:"mine-pid", agent_id:"mine", host:me, state:"active" },
  { pid:"their-pid", agent_id:"theirs", host:vitalii, state:"active" }];
const ps = m.participants({ peer:vitalii, agents }, o, names).filter(p => p.kind === "guest-agent");
assert.deepEqual(ps.map(p => [p.pid,p.name]), [["mine-pid","Your Codex"],["their-pid","Vitalii’s Codex"]]);
