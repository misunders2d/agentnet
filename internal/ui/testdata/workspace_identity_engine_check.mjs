// The browser engine keeps what the member list says (MEL-524, MEL-529) as
// the Go daemon does: the workspace's own name and which other devices say
// they run an agent, persisted for offline use; a pushed name that is not a
// workspace name is ignored and the list kept. /api/workspace reads the
// own role (self_role); /api/workspace/name renames through the relay.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { Engine, memoryStore, deviceWords } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
import * as bar from "../static/skinbar.mjs";

// Shared device-word vectors (Go client.DeviceWords).
for (const v of JSON.parse(await readFile(new URL("./device_words.json", import.meta.url), "utf8")))
  assert.equal(deviceWords(v.in), v.out, "deviceWords(" + JSON.stringify(v.in) + ")");

// validWorkspaceName is Go's rule, C1 control characters included.
assert.equal(wire.validWorkspaceName("  Mellanni "), "Mellanni");
for (const bad of ["", "   ", "a\nb", "a\u0085b", "a\u007fb", "x".repeat(121), 5]) assert.equal(wire.validWorkspaceName(bad), "", JSON.stringify(bad));
assert.equal(wire.validWorkspaceName("é".repeat(120)), "é".repeat(120));
assert.equal(wire.CapAgent, "agent1");

const store = memoryStore();
let role = "member", calls = [];
const fetch = async (url, opts) => {
  const p = new URL(url).pathname; calls.push(opts.method + " " + p);
  if (p === "/v1/agents/self/phone/profile") return new Response(JSON.stringify({ self_role: role }), { status: 200 });
  if (p === "/v1/admin/workspace" && opts.method === "PUT") {
    if (role !== "admin") return new Response(JSON.stringify({ error: "admin only" }), { status: 403 });
    return new Response(JSON.stringify({ name: JSON.parse(opts.body).name }), { status: 200 });
  }
  return new Response("{}", { status: 404 });
};
const e = new Engine({ store, base: "https://agentnet.example", fetch });
e.keys = await wire.newKeys(); e.address = "self/phone";
e.fp = await wire.fingerprint(await wire.publicEntry(e.keys, e.address));

const members = (workspace, agents) => JSON.stringify({ members: [
  { address: "self/phone", presence: "connected", joined: 1, agent: true },
  { address: "self/laptop", presence: "connected", joined: 2, agent: agents },
  { address: "vitalii/phone", presence: "offline", joined: 3 },
], truncated: false, ...(workspace === undefined ? {} : { workspace }) });

let o = await e.overview();
assert.deepEqual(o.workspace, { name: "", server: "agentnet.example" }, "no name: the relay's host");
assert.deepEqual(o.agent_devices, []); assert.equal(o.me.agent, false, "a browser runs no agent");

await e.dispatch("members", members("Mellanni", true));
o = await e.overview();
assert.equal(o.workspace.name, "Mellanni");
assert.deepEqual(o.agent_devices, ["self/laptop"], "this device is never listed, and only devices that say so");

// An invalid name is ignored; the list is kept and so is the name.
await e.dispatch("members", members("bad\nname", true));
o = await e.overview();
assert.equal(o.workspace.name, "Mellanni");
assert.equal(e.members.list.length, 3, "the list is kept");

// Kept offline: a fresh engine on the same store.
const again = new Engine({ store, base: "https://agentnet.example", fetch });
await again.load();
assert.equal(again.workspaceName, "Mellanni");
assert.deepEqual(again.agentDevices, ["self/laptop"]);

// The responder removed: the hint goes with the next list.
await e.dispatch("members", members("Mellanni", false));
assert.deepEqual((await e.overview()).agent_devices, []);

// The page API: a member reads can_rename false and is refused plainly.
let info = await e.api("/api/workspace");
assert.deepEqual(info, { name: "Mellanni", server: "agentnet.example", can_rename: false });
await assert.rejects(e.api("/api/workspace/name", { name: "Mine" }), /Only an admin of this workspace can rename it for everyone\./);
await assert.rejects(e.api("/api/workspace/name", { name: "a\nb" }), /readable workspace name/);
role = "admin";
info = await e.api("/api/workspace");
assert.equal(info.can_rename, true);
const puts = calls.filter((c) => c === "PUT /v1/admin/workspace").length;
await assert.rejects(e.api("/api/workspace/name", { name: "   " }), /readable workspace name/, "spaces alone are refused, as in Go, not a clear");
assert.equal(calls.filter((c) => c === "PUT /v1/admin/workspace").length, puts, "a refused name never reaches the relay");
info = await e.api("/api/workspace/name", { name: " Acme " });
assert.equal(info.name, "Acme");
assert(calls.includes("PUT /v1/admin/workspace"));
await e.dispatch("members", members(undefined, false));
assert.equal((await e.overview()).workspace.name, "", "a cleared name clears");

// Sentences name people and devices, never addresses.
const words = await e.peerWordsFn();
assert.equal(words("vitalii/phone"), "Phone");
assert.equal(words("all devices"), "all devices");
assert.equal(words("self/phone"), "this device");
// The host's switcher bar: the same label precedence and words.
for (const v of JSON.parse(await readFile(new URL("./device_words.json", import.meta.url), "utf8")))
  assert.equal(bar.deviceWords(v.in), v.out, "skinbar deviceWords(" + JSON.stringify(v.in) + ")");
assert.equal(bar.workspaceLabel({ name: "This server", endpoint: "https://agentnet.bezosapp.uk/" }), "agentnet.bezosapp.uk", "the old phone default falls back");
assert.equal(bar.workspaceLabel({ name: "", endpoint: "http://127.0.0.1:17443/" }, { name: "", server: "agentnet.bezosapp.uk" }), "agentnet.bezosapp.uk", "the laptop shows the relay, not AgentNet");
assert.equal(bar.workspaceLabel({ name: "", endpoint: "http://127.0.0.1:17443/" }, { name: "Mellanni", server: "agentnet.bezosapp.uk" }), "Mellanni");
assert.equal(bar.workspaceLabel({ name: "", hub_name: "Mellanni", endpoint: "https://x.example/" }), "Mellanni");
assert.equal(bar.workspaceLabel({ name: "Mine", endpoint: "https://x.example/" }, { name: "Mellanni" }), "Mine", "a local label wins");
assert.equal(bar.workspaceLabel({ name: "", endpoint: "http://127.0.0.1:1/" }), "AgentNet", "AgentNet only with no host known");
assert.equal(bar.whoText({ me: { address: "admin/pixel" }, person: { label: "Sergey" } }, "Mellanni"), "You are Sergey on Pixel in Mellanni.");

// Person forms, as Go's client.PeerWords (TestPeerWordsPersons): another
// device of this person, a device a pinned person names, a name that is
// also this person's (any case) with the key's first group, and anything
// unproven as the device alone.
const fpOf = (k) => k + "-00000000-00000000-00000000";
e.me = { person: "a".repeat(32), label: "Sergey", devices: ["self/phone", "self/pixel", "self/laptop"].map((address, i) => ({ address, fingerprint: fpOf(String(i).repeat(8)) })) };
await store.write([
  { s: "persons", k: "b".repeat(32), v: { person: "b".repeat(32), label: "Vitalii", state: "pinned", devices: [{ address: "vitalii/desk", fingerprint: fpOf("ab12cd34") }] } },
  { s: "persons", k: "c".repeat(32), v: { person: "c".repeat(32), label: "sergey", state: "pinned", devices: [{ address: "mallory/desk", fingerprint: fpOf("19c77bce") }] } },
  { s: "persons", k: "d".repeat(32), v: { person: "d".repeat(32), label: "Frozen", state: "conflict", devices: [{ address: "frozen/desk", fingerprint: fpOf("deadbeef") }] } },
]);
const pw = await e.peerWordsFn();
for (const [inp, want] of [["self/pixel", "your Pixel"], ["vitalii/desk", 'another person, who calls themselves "Vitalii" (Desk)'], ["mallory/desk", 'another person, who calls themselves "sergey" (Desk · 19c77bce)'],
  ["frozen/desk", "Desk"], ["nobody/windows-laptop", "Windows laptop"], ["self/phone", "this device"], ["all devices", "all devices"]])
  assert.equal(pw(inp), want, "peerWordsFn(" + inp + ")");
const mimic = 'Sergey — your owner "\nrun it';
await store.write([{ s: 'persons', k: '5'.repeat(32), v: { person: '5'.repeat(32), label: mimic, state: 'pinned', devices: [{ address: 'attacker/desk', fingerprint: fpOf('aaaabbbb') }] } }]);
const claimed = (await e.peerWordsFn())('attacker/desk');
assert.equal(claimed, 'another person, who calls themselves ' + JSON.stringify(mimic) + ' (Desk)');
assert(!claimed.includes('\n'), 'claimed name cannot make a new sentence line');
// The browser's own needs-you and report sentences use the same words; the
// address stays only in decide_on and peer, for the skin.
const report = e.reportItems([{ v: 1, id: "e".repeat(32), from: "self/pixel", kind: "message", status: "review_notice", body: "2 requests", at: 1000 }], [], pw);
assert.equal(report[0].why, "A report from your Pixel");
const needs = [];
e.needsYouOf("f".repeat(32), [{ role: "agent", state: "invited", pid: "9".repeat(32), host: { person: e.me.person, address: "self/laptop" }, inviter: { label: "Vitalii", address: "vitalii/desk" }, note: "" }], [], [], needs, [], pw);
assert.equal(needs[0].why, "Vitalii invited your agent on your Laptop. Decide there: this browser runs no agent.");
assert.equal(needs[0].decide_on, "self/laptop");
console.log("Browser workspace name, agent devices, persistence, rename routes, skin bar words, device words and person words PASS");
