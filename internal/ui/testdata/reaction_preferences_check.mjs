import assert from "node:assert/strict";
import { recordReaction, reactionChoices, humanReactionOrder } from "../skins/shared/reaction-preferences.mjs";

const key = "agentnet.messenger.reaction-usage.v1", saved = new Map();
const storage = () => ({ getItem: k => saved.get(k) ?? null, setItem: (k, v) => saved.set(k, v) });
const fallback = ["👍", "❤️", "😂", "🎉", "🙏", "👀"];
assert.deepEqual(reactionChoices(fallback, 6, storage), fallback, "first use retains familiar choices");
assert.equal(saved.size, 0, "rendering never saves usage");
for (const e of ["👩🏽‍💻", "👀", "👀", "👩🏽‍💻", "👀"]) recordReaction(e, storage);
assert.deepEqual(reactionChoices(fallback, 6, storage), ["👀", "👩🏽‍💻", "👍", "❤️", "😂", "🎉"]);
const reloaded = () => ({ getItem: k => JSON.parse(JSON.stringify(Object.fromEntries(saved)))[k] ?? null });
assert.deepEqual(reactionChoices([], 8, reloaded), ["👀", "👩🏽‍💻"], "reload retains exact toned/joined emoji");
recordReaction("👩🏽‍💻", storage);
assert.deepEqual(reactionChoices([], 8, storage), ["👩🏽‍💻", "👀"], "ties retain first-use order");
const before = saved.get(key);
reactionChoices(fallback, 6, storage);
assert.equal(saved.get(key), before);

for (const raw of ["{", "null", "{}", '"bad"', "[]", " ".repeat(16385)]) {
  saved.set(key, raw);
  assert.deepEqual(reactionChoices(fallback, 6, storage), fallback);
  recordReaction("🎉", storage);
  assert.deepEqual(reactionChoices([], 8, storage), ["🎉"]);
}
saved.set(key, JSON.stringify([["👍", 0], ["👀", 1.5], ["😂", -1], ["🎉", 1000001], null, {}, ["🙏", 2], ["🙏", 9], ["", 3], ["x".repeat(129), 1]]));
assert.deepEqual(reactionChoices([], 8, storage), ["🙏"], "invalid/duplicate entries are ignored");
for (const denied of [() => { throw Error("Denied"); }, () => ({ getItem: () => { throw Error("Denied"); }, setItem: () => { throw Error("Denied"); } }), () => null]) {
  assert.deepEqual(reactionChoices(fallback, 6, denied), fallback);
  assert.doesNotThrow(() => recordReaction("👍", denied));
}
saved.set(key, JSON.stringify([["👍", 1000000]]));
recordReaction("👍", storage);
assert.equal(JSON.parse(saved.get(key))[0][1], 1000000, "counts stay bounded");
saved.clear();
for (let i = 0; i < 70; i++) recordReaction(String.fromCodePoint(0x1f600 + i), storage);
assert.equal(JSON.parse(saved.get(key)).length, 64);
assert.equal(reactionChoices([], 64, storage).at(-1), String.fromCodePoint(0x1f600 + 69), "new choices can enter the bounded history");

const human = id => ({ id }), agent = { id: "agent", assistant: true };
const list = [{ emoji: "😂", by: [human("a")] }, { emoji: "👀", by: [agent, agent, agent, agent] }, { emoji: "👍", mine: true, by: [human("a"), human("b"), agent] }, { emoji: "🎉", by: [human("a"), human("b")] }];
const original = structuredClone(list), sorted = humanReactionOrder(list);
assert.deepEqual(sorted.map(r => r.emoji), ["👍", "🎉", "😂", "👀"], "human counts only; stable ties");
assert.deepEqual(list, original, "received projection is not mutated");
assert.equal(sorted[0], list[2], "identity, mine, and separate agent reactors are unchanged");
console.log("reaction preferences: persisted exact emoji, stable order, bounded/malformed/denied storage, immutable human-count ordering passed");
