// Comic's people model (MEL-529, MEL-525): a device is an agent only when it
// says it runs one; own devices are you; other people's devices are those
// people; look-alike names get a short key. model.ts holds every decision
// as pure helpers; the fixture uses the shipped Markdown projection.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import m from "./comic_model.mjs";

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
const legacy = (host, key) => ({ agent_host:host, agent_key:key, agent_id:"", agent_legacy:true });

// runsAgent: only what the device says; a phone never.
assert.equal(m.runsAgent("admin/pixel", o), false);
assert.equal(m.runsAgent("admin/zenbook", o), true);
assert.equal(m.runsAgent("admin/laptop", o), true, "this device: me.agent");
assert.equal(m.runsAgent("admin/laptop", laptop(false)), false);
assert.deepEqual([...m.agentPeers(o)].sort(), ["admin/zenbook", "bohdan/windows-laptop", "vitalii/desk"]);
assert.equal(m.runsAgent("x/y", { ...o, agent_devices: null }), false, "a null list is no agent");

// Chat rows: the own phone is never "Your agent · on Pixel".
let r = m.threadRow("admin/pixel", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle, r.local], ["agent", "Agent history", "On this computer · asked from your Pixel", true]);
r = m.threadRow("admin/pixel", laptop(false), {});
assert.deepEqual([r.kind, r.title], ["person", "Your Pixel"], "a computer running no agent: the phone is you");
r = m.threadRow("admin/zenbook", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Your agent history on Zenbook", "on Zenbook"]);
r = m.threadRow("admin/zenbook", { ...laptop(false), agent_devices: [] }, {});
assert.deepEqual([r.kind, r.title], ["person", "Your Zenbook"], "its responder removed: your Zenbook (seen from a device running none)");
r = m.threadRow("admin/zenbook", { ...o, agent_devices: [] }, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Agent history", "On this computer · asked from your Zenbook"], "seen from a computer running an agent: its questions come here");
r = m.threadRow("vitalii/phone", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["person", "Vitalii", "from Phone"]);
r = m.threadRow("vitalii/desk", o, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Vitalii’s agent history on Desk", "on Desk"]);
r = m.threadRow("hub/bezos", { ...o, agent_devices: ["hub/bezos"] }, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Bezos", "Agent · not linked to a person"]);
const list = m.chatList({ ...o, threads: [{ id: "t1", peer: "admin/pixel", title: "x", last: "hi", last_at: "2026-10-05T10:00:00Z", count: 1, review: 0, unread: 0, running: 0, waiting: false, key_changed: false, notices: 0 }] }, {});
assert.equal(list.length, 0, "unbound agent history is not an active recipient");

// A device you removed from your roster (admin/phone-3 replaced) is history,
// not a "Not linked to a person" chat: its unbound device thread leaves the
// active list, on a computer with or without an agent; its name stays yours.
const phoneThread = { id: "old1", peer: "admin/phone-3", agent_host: "admin/phone-3", title: "x", last: "hi", last_at: "2026-10-18T10:00:00Z", count: 1, review: 0, unread: 0, running: 0, waiting: false, key_changed: false, notices: 0, state: "archived" };
for (const agent of [true, false]) {
  const seen = laptop(agent);
  assert.equal(m.chatList({ ...seen, threads: [phoneThread] }, {}).length, 1, "before: the unlinked device thread was listed (agent=" + agent + ")");
  const gone = { ...seen, removed_devices: [{ address: "admin/phone-3", person: "p-me" }] };
  assert.equal(m.chatList({ ...gone, threads: [phoneThread] }, {}).length, 0, "a removed own device's thread is not an active chat (agent=" + agent + ")");
  assert.equal(m.chatList({ ...gone, threads: [], topics: [{ peer: "admin/phone-3", total: 3, archived: 3, archived_unread: 0, latest: phoneThread }] }, {}).length, 0, "nor its archived topics (agent=" + agent + ")");
  r = m.threadRow("admin/phone-3", gone, {});
  assert.deepEqual([r.kind, r.title, r.subtitle], ["person", "Your Phone 3", "Removed device"], "history keeps it yours (agent=" + agent + ")");
  // Never "you" for review or asker words: a removed (maybe lost) phone lost that access.
  assert.equal(m.deviceWho("admin/phone-3", gone).relation, "unknown");
}
// This computer's current agent, once asked from the removed phone, keeps its row.
const aid = "0123456789abcdef0123456789abcdef";
const bound = { ...phoneThread, id: "old2", agent_host: "admin/laptop", agent_key: "11111111-a", agent_id: aid };
const agentNames = { [aid]: "Claude", [aid + "#current"]: "true", [aid + "#host"]: "admin/laptop", [aid + "#host_key"]: "11111111-a" };
assert.equal(m.chatList({ ...o, removed_devices: [{ address: "admin/phone-3", person: "p-me" }], threads: [bound] }, agentNames).length, 1, "a current agent's topic asked from it stays listed");
// An agent hosted on a removed device is history too: the row's device is its host.
const hostedThere = { ...phoneThread, id: "old3", agent_key: "99999999-z", agent_id: aid };
const thereNames = { [aid]: "Claude", [aid + "#current"]: "true", [aid + "#host"]: "admin/phone-3", [aid + "#host_key"]: "99999999-z" };
assert.equal(m.chatList({ ...o, removed_devices: [{ address: "admin/phone-3", person: "p-me" }], threads: [hostedThere] }, thereNames).length, 0, "an agent row hosted on the removed device leaves the list");
// A device a current roster names is never taken for a removed one.
r = m.threadRow("vitalii/phone", { ...o, removed_devices: [{ address: "vitalii/phone", person: "p-vit" }] }, {});
assert.deepEqual([r.title, r.subtitle], ["Vitalii", "from Phone"]);
assert.equal(m.removedDevice("admin/phone-3", { ...o, person: undefined, removed_devices: [{ address: "admin/phone-3", person: "p-me" }] }), null, "no person here: nothing is yours");
// Another person's removed device (owner 2026-11-27, "same as mine"): Vitalii
// dropped vitalii/old-phone from his signed roster; its chat leaves the list,
// its row (search) names it as his, never "Not linked to a person" or you.
const oldPhone = { ...phoneThread, id: "old4", peer: "vitalii/old-phone", agent_host: "vitalii/old-phone" };
for (const agent of [true, false]) {
  const seen = laptop(agent);
  assert.equal(m.chatList({ ...seen, threads: [oldPhone] }, {}).length, 1, "before: another person's dropped device was listed (agent=" + agent + ")");
  const gone = { ...seen, removed_devices: [{ address: "vitalii/old-phone", person: "p-vit" }] };
  assert.equal(m.chatList({ ...gone, threads: [oldPhone] }, {}).length, 0, "another person's removed device is not an active chat (agent=" + agent + ")");
  r = m.threadRow("vitalii/old-phone", gone, {});
  assert.deepEqual([r.kind, r.title, r.subtitle], ["person", "Vitalii’s Old phone", "Removed device"], "history names it as Vitalii's (agent=" + agent + ")");
  assert.notEqual(m.deviceWho("vitalii/old-phone", gone).relation, "own");
}

// Message authors in the laptop↔Pixel thread.
const q = { id: "q1", dir: "in", from: "admin/pixel", kind: "question", body: "?", state: "answered" };
const a = { id: "a1", dir: "out", from: "admin/laptop", kind: "answer", reply_to: "q1", body: "!" };
const typed = { id: "m1", dir: "in", from: "admin/pixel", kind: "message", body: "thanks" };
let w = m.threadAuthor(q, o, {}, "admin/pixel", [q, a]);
assert.deepEqual([w.name, w.sub, w.mine, w.agent], ["You", "from Pixel", true, false], "a question typed on the phone is yours");
w = m.threadAuthor(typed, o, {}, "admin/pixel", [q, a, typed]);
assert.deepEqual([w.name, w.mine], ["You", true], "plain messages too");
w = m.threadAuthor(a, o, {}, "admin/pixel", [q, a]);
assert.deepEqual([w.name, w.agent, w.mine], ["Agent history · identity unknown", true, false], "only the laptop agent's answers are on the left; its earlier identity is unknown");
w = m.threadAuthor(a, o, {}, "admin/pixel", [{ ...q, state: "manual" }, a]);
assert.deepEqual([w.name, w.mine], ["You", true], "answered by hand: you");
w = m.threadAuthor({ id: "v", dir: "in", from: "vitalii/phone", kind: "message", body: "hi" }, o, {}, "vitalii/phone");
assert.deepEqual([w.name, w.sub, w.mine, w.agent], ["Vitalii", "from Phone", false, false]);
w = m.threadAuthor({ id: "z", dir: "in", from: "admin/zenbook", kind: "answer", body: "ok" }, o, {}, "admin/zenbook");
assert.deepEqual([w.name, w.agent], ["Agent history · identity unknown", true]);
w = m.threadAuthor({ id: "o", dir: "out", from: "admin/laptop", kind: "question", body: "?" }, o, {}, "admin/zenbook");
assert.deepEqual([w.name, w.mine], ["You", true]);

// The composer: no Ask toward a device that runs no agent.
assert.deepEqual(m.deviceTarget("admin/zenbook", o), { kind: "agent" });
assert.equal(m.deviceTarget("admin/pixel", o).kind, "own");
assert.match(m.deviceTarget("admin/pixel", o).note, /your own Pixel\. Questions you ask there come to this computer’s agent\./);
assert.deepEqual(m.deviceTarget("vitalii/phone", o), { kind: "person", name: "Vitalii" });

// Bring in: neither a browser nor an advertised device invents a default recipient.
assert.deepEqual(m.bringInDevices(bohdan, o, {}), [], "an advertised agent device without a concrete catalog entry is not a recipient");
assert.deepEqual(m.bringInDevices(bohdan, o, { "bohdan/windows-laptop": [{ id: "x" }] }), [], "named agents are offered one by one");
assert.deepEqual(m.bringInDevices(vitalii, o, {}), [], "a verified human's device still needs a concrete agent selection");

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
assert.deepEqual(m.askerWords("admin/zenbook", o, {}), { name:"Your agent history on Zenbook", device:"Zenbook", agent:true, you:false }, "the live no-binding caller names the peer host, not its owner's primary device");

// A phone (a browser runs no agent): the thread with the laptop is your agent there.
const phone = { ...laptop(false), me: { address: "admin/pixel", fingerprint: "19c77bce-b", responder: "", responder_dir: "", agent: false }, agent_devices: ["admin/laptop"] };
r = m.threadRow("admin/laptop", phone, {});
assert.deepEqual([r.kind, r.title, r.subtitle], ["agent", "Your agent history on Laptop", "on Laptop"]);
w = m.threadAuthor({ id: "o", dir: "out", from: "admin/pixel", kind: "question", body: "?" }, phone, {}, "admin/laptop");
assert.equal(w.name, "You");

// A concrete historical target stays distinct from the current local Claude default.
const concreteID = "a".repeat(32), concreteNames = { [concreteID]:"Pi", [concreteID+"#harness"]:"pi" };
r = m.threadRow("admin/zenbook", o, concreteNames, { ...legacy("admin/zenbook", "22222222-c"), agent_id:concreteID, agent_legacy:false });
assert.deepEqual([r.kind, r.title, r.subtitle, r.local], ["agent", "Pi", "on Zenbook", false]);
w = m.threadAuthor({ ...a, agent_id:concreteID }, o, concreteNames, "admin/pixel", [q, a]);
assert.deepEqual([w.name, w.agent, w.mine], ["Pi", true, false], "named historical output does not inherit today's default program");
console.log("Comic people model: agents only where one runs, own devices are you, people are people, look-alikes keyed, Bring in agents only PASS");

// Same-named assistants remain distinguishable in compact labels and mentions.
const names = { mine: "Codex", theirs: "Codex" };
assert.equal(m.agentName("mine", names, me, me), "Your Codex");
assert.equal(m.agentName("theirs", names, vitalii, me), "Vitalii’s Codex");
const agents = [{ pid:"mine-pid", agent_id:"mine", host:me, state:"active" },
  { pid:"their-pid", agent_id:"theirs", host:vitalii, state:"active" }];
const ps = m.participants({ peer:vitalii, agents }, o, names).filter(p => p.kind === "guest-agent");
// Owner, 2026-11-09: whose, which and where, so two of one person's agents never read alike.
assert.deepEqual(ps.map(p => [p.pid,p.name]), [["mine-pid","Your Codex · Laptop"],["their-pid","Vitalii’s Codex · Desk"]]);
const two = m.participants({ peer:vitalii, agents:[{ pid:"a", agent_id:"mine", host:me, state:"active" }, { pid:"b", agent_id:"mine2", host:{ ...me, address:"admin/bezos" }, state:"active" }] }, o, { mine:"Pi", mine2:"Pi" }).filter(p => p.kind === "guest-agent");
assert.deepEqual(two.map(p => p.name), ["Your Pi · Laptop", "Your Pi · Bezos"], "two own agents of one name differ by device");
