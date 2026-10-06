// Classic and Zoom carry their own copy of the people words (standalone
// modules: they see only the host API). This runs each copy's pure block,
// as written in its entry.mjs, against the shared device-word vectors and
// the same people rules as Comic's model.ts.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const vectors = JSON.parse(await readFile(new URL("./device_words.json", import.meta.url), "utf8"));
const me = { person: "p-me", label: "Sergey", address: "admin/laptop", state: "self", devices: [{ address: "admin/laptop" }, { address: "admin/pixel", fingerprint: "19c77bce-b" }] };
const o = { me: { address: "admin/laptop", agent: true }, person: me, agent_devices: ["vitalii/desk"],
  people: [{ person: "p-vit", label: "Vitalii", address: "vitalii/desk", state: "pinned", devices: [{ address: "vitalii/desk" }, { address: "vitalii/phone", fingerprint: "44444444-e" }] },
    { person: "p-fake", label: "sergey", address: "mallory/box", state: "pinned", devices: [{ address: "mallory/box", fingerprint: "77777777-h" }] },
    { label: "Bohdan", address: "bohdan/laptop-browser", state: "listed", devices: [{ address: "bohdan/laptop-browser", fingerprint: "66666666-g" }] }] };
for (const skin of ["classic", "zoom"]) {
  const src = await readFile(new URL("../skins/" + skin + "/src/entry.mjs", import.meta.url), "utf8");
  const start = src.indexOf("// ---- people words"), end = src.indexOf("// ---- end of people words");
  assert(start > 0 && end > start, skin + ": people words block");
  const words = new Function(src.slice(start, end) + "; return { deviceWords, runsAgentIn, whoParts, whoTextIn, devicesText, whoMatches };")();
  for (const v of vectors) assert.equal(words.deviceWords(v.in), v.out, skin + " deviceWords(" + JSON.stringify(v.in) + ")");
  assert.equal(words.runsAgentIn(o, "admin/pixel"), false, skin + ": a phone is no agent");
  assert.equal(words.runsAgentIn(o, "vitalii/desk"), true);
  assert.equal(words.runsAgentIn(o, "admin/laptop"), true, skin + ": this device, from me.agent");
  assert.equal(words.whoTextIn(o, "admin/pixel"), "You · Pixel", skin);
  assert.equal(words.whoTextIn(o, "vitalii/phone"), "Vitalii · Phone", skin);
  assert.equal(words.whoTextIn(o, "mallory/box"), "sergey · 77777777 · Box", skin + ": a name like yours shows its key");
  assert.equal(words.whoTextIn(o, "bohdan/laptop-browser"), "Bohdan · 66666666 · Laptop browser", skin + ": a listed name shows its key");
  assert.equal(words.whoTextIn(o, "hub/bezos"), "Bezos", skin + ": no person: the device");
  assert(!/\b[a-z]+\/[a-z]+\b/.test(words.whoTextIn(o, "vitalii/desk")), skin + ": never an address");
  assert.equal(words.devicesText(o.people[0]), "on Desk, Phone", skin + ": a person's devices in words");
  assert.equal(words.devicesText({ address: "bohdan/windows-laptop" }), "on Windows laptop", skin + ": one device, no list");
  assert.equal(words.whoMatches(o, "vitalii/phone", "vitalii"), true, skin + ": search finds a device by its person");
  assert.equal(words.whoMatches(o, "admin/pixel", "pixel"), true);
  assert.equal(words.whoMatches(o, "vitalii/phone", "sergey"), false);
  // Main screens and search say who, never "via <address>", and group
  // devices as devices.
  assert(!/via " \+ (p|d\.peer)\.address|· via " \+ p\.address|'s, on " \+ t\.peer\.address/.test(src), skin + ": an address on a person screen");
  assert(!src.includes('plural(agents.length + listed.length, "agent", "agents")'), skin + ": search rows are people and agents, headed as agents");
}
console.log("Classic and Zoom people words match the shared vectors and rules PASS");
