import test from "node:test";
import assert from "node:assert/strict";
import { isSendPersisted } from "./send-recovery.mjs";

const id = "a".repeat(32), parentID = "b".repeat(32), conv = "c".repeat(64);
const me = { address: "test/phone", fingerprint: "d".repeat(64) };
const peer = "test/laptop";
const physical = "e".repeat(32), topic = "f".repeat(32);
const rawEntry = () => ({ id, conversation: parentID, endpoint: "/api/send",
  request: { id, to: peer, kind: "question", body: "  original  ", reply_to: parentID } });
const rawMessage = () => ({ id, dir: "out", from: me.address, from_key: me.fingerprint,
  to: peer, kind: "question", body: "original", reply_to: parentID });
const dmEntry = () => ({ id, conversation: conv, endpoint: "/api/dm/send", request: { id, conv, body: "original" } });
const dmMessage = () => ({ id: physical, lid: id, dir: "out", from: me.address,
  send_group_author: me.fingerprint, kind: "message", body: "original" });
function host(views) {
  const calls = [];
  return { calls, async api(path, body) {
    assert.equal(body, undefined, "recovery must never write or replay");
    calls.push(path);
    if (path === "/api/overview") return structuredClone({ me, dms: [], ...views.overview });
    const value = views[path];
    if (!value) throw Error("not stored");
    return structuredClone(value);
  } };
}
const rawHost = message => host({ ["/api/thread?id=" + id]: { id: parentID, peer, messages: [message] } });
const dmHost = (message, more = {}) => host({ ["/api/dm?id=" + conv]: { id: conv, messages: [message], agents: [], ...more } });

test("lost admission response uses exact ID outside latest thread page; edited display is irrelevant", async () => {
  const m = { ...rawMessage(), edited: true, deleted: true, text: "Deleted message", state: "not_delivered" };
  const h = rawHost(m);
  assert.equal(await isSendPersisted(h, rawEntry()), true);
  assert.deepEqual(h.calls, ["/api/overview", "/api/thread?id=" + id]);
});

test("raw requests require their own exact human signature, body, recipient, reply, quote and kind", async () => {
  for (const change of [
    { id: parentID }, { dir: "in" }, { from: peer }, { from_key: "foreign" }, { from_key: undefined },
    { kind: "task" }, { body: "different", text: "original" }, { to: "test/other" },
    { reply_to: id }, { quote: parentID }, { agent_id: "author" }, { agent_author_pid: "author" },
    { verified_agent: true }, { history: true }, { synced_from: peer }, { claimed_key: me.fingerprint },
    { excerpt_pid: "unverified-excerpt" }, { files: [{ name: "file", size: 1 }] },
    { attachments: [{ name: "file", size: 1 }] }, { target: { address: peer, agent_id: "executor" } },
  ]) assert.equal(await isSendPersisted(rawHost({ ...rawMessage(), ...change }), rawEntry()), false, JSON.stringify(change));
});

test("raw agent executor is target.agent_id, never the outgoing named author", async () => {
  const e = rawEntry(); e.request.agent_id = "executor"; e.request.quote = parentID;
  const m = { ...rawMessage(), quote: parentID, target: { address: peer, agent_id: "executor" } };
  assert.equal(await isSendPersisted(rawHost(m), e), true);
  assert.equal(await isSendPersisted(rawHost({ ...m, agent_id: "executor" }), e), false);
  assert.equal(await isSendPersisted(rawHost({ ...m, target: { address: "test/other", agent_id: "executor" } }), e), false);
});

test("original body comparison follows Go whitespace rather than display/JS normalization", async () => {
  const e = rawEntry(); e.request.body = "\u0085original\u0085";
  assert.equal(await isSendPersisted(rawHost(rawMessage()), e), true);
  e.request.body = "\ufefforiginal\ufeff";
  assert.equal(await isSendPersisted(rawHost(rawMessage()), e), false);
  assert.equal(await isSendPersisted(rawHost({ ...rawMessage(), body: e.request.body }), e), true);
});

test("DM admission matches logical ID rather than a physical copy ID", async () => {
  const e = dmEntry(), m = dmMessage();
  assert.equal(await isSendPersisted(dmHost({ ...m, text: "Edited", edited: true }), e), true);
  assert.equal(await isSendPersisted(dmHost({ ...m, lid: parentID, id }), e), false);
  for (const change of [{ from: peer }, { send_group_author: "foreign" }, { send_group_author: undefined },
    { pid: "other" }, { topic }, { send_group: parentID }, { agent_id: "named-author" },
    { target: { address: peer } }, { body: "different" }, { reply_to: parentID }, { quote: parentID }]) {
    assert.equal(await isSendPersisted(dmHost({ ...m, ...change }), e), false, JSON.stringify(change));
  }
  assert.equal(await isSendPersisted(dmHost(m, { id: "other-root" }), e), false);
});

test("DM original reply and quote survive physical/logical projection mapping; ambiguous IDs stay pending", async () => {
  const e = dmEntry(); e.request.reply_to = parentID; e.request.quote = parentID;
  const parent = { id: "1".repeat(32), lid: parentID, kind: "message", body: "parent" };
  const m = { ...dmMessage(), reply_to: parent.id, quote: parent.id };
  const h = dmHost(m, { messages: [parent, m] });
  assert.equal(await isSendPersisted(h, e), true);
  assert.equal(await isSendPersisted(dmHost(m, { messages: [parent, { ...parent, id: "2".repeat(32) }, m] }), e), false);
});

test("explicit human participation and original topic must match", async () => {
  const e = dmEntry(); e.request.pid = "human-participation"; e.request.topic = topic;
  const m = { ...dmMessage(), pid: e.request.pid, topic };
  assert.equal(await isSendPersisted(dmHost(m), e), true);
  assert.equal(await isSendPersisted(dmHost({ ...m, pid: "other" }), e), false);
  assert.equal(await isSendPersisted(dmHost(m, { messages: [m, { topic_event: { action: "move" } }] }), e), false);
  // The projection cannot prove a core-selected implicit parent or allocated topic.
  assert.equal(await isSendPersisted(dmHost({ ...m, reply_to: parentID }), e), false);
  assert.equal(await isSendPersisted(dmHost(m), { ...e, request: { ...e.request, topic: "new" } }), false);
  const parent = { id: parentID, kind: "message", topic };
  assert.equal(await isSendPersisted(dmHost(m, { messages: [parent, { ...m, reply_to: parentID }] }),
    { ...e, request: { ...e.request, reply_to: parentID } }), false,
    "display inheritance cannot prove an explicit original topic");
});

test("agent request finds exact PID root beyond aggregated chat, preserving executor, group and original topic", async () => {
  const other = "9".repeat(64), pid = "agent-participation", group = "8".repeat(32);
  const agent = { pid, agent_id: "executor", host: { address: peer, fingerprint: "7".repeat(64) } };
  const e = { ...dmEntry(), endpoint: "/api/dm/agent/ask", request: { id, pid, body: "original", kind: "question", topic, send_group: group } };
  const m = { ...dmMessage(), kind: "question", pid, topic, send_group: group,
    target: { address: peer, fingerprint: agent.host.fingerprint, agent_id: agent.agent_id } };
  const makeHost = message => host({ overview: { dms: [{ id: other }] },
    ["/api/dm?id=" + conv]: { id: conv, messages: [], agents: [] },
    ["/api/dm?id=" + other]: { id: other, messages: [message], agents: [agent] } });
  assert.equal(await isSendPersisted(makeHost(m), e), true);
  for (const change of [{ pid: "different" }, { send_group: "different" }, { topic: parentID },
    { target: { ...m.target, agent_id: "different" } }, { target: { ...m.target, fingerprint: "different" } }]) {
    assert.equal(await isSendPersisted(makeHost({ ...m, ...change }), e), false);
  }
});

test("manual reply proof binds saved send ID to original inbox question/task and raw body", async () => {
  const e = { id, conversation: parentID, endpoint: "/api/act", request: { do: "reply", id: parentID, send_id: id, body: " original " } };
  const parent = { id: parentID, dir: "in", from: peer, kind: "task" };
  const m = { ...rawMessage(), kind: "result", body: e.request.body, status: "done" };
  const h = message => rawHost(message);
  const views = message => host({ ["/api/thread?id=" + id]: { id: parentID, peer, messages: [parent, message] } });
  assert.equal(await isSendPersisted(views(m), e), true);
  assert.equal(await isSendPersisted(h(m), e), false, "missing original inbox target");
  assert.equal(await isSendPersisted(views({ ...m, status: "declined" }), e), false);
  assert.equal(await isSendPersisted(views({ ...m, body: "original" }), e), false);
  assert.equal(await isSendPersisted(views(m), { ...e, request: { ...e.request, do: "accept" } }), false);
});

test("unknown authority fields, changed IDs, attachment stages and lookup failures never acknowledge", async () => {
  for (const request of [
    { ...rawEntry().request, reply_receiver: {} }, { ...rawEntry().request, accept: "yes" },
    { ...rawEntry().request, id: parentID }, { ...rawEntry().request, files: ["stage-exact"] },
    { ...rawEntry().request, files: null }, { ...rawEntry().request, kind: null },
  ]) {
    const h = rawHost(rawMessage());
    assert.equal(await isSendPersisted(h, { ...rawEntry(), request }), false);
    assert.deepEqual(h.calls, [], "invalid/unprovable intent must not start lookups");
  }
  assert.equal(await isSendPersisted({ api: async () => { throw Error("offline"); } }, rawEntry()), false);
  const h = host({ overview: { me: {} } });
  assert.equal(await isSendPersisted(h, rawEntry()), false);
  const duplicate = rawMessage();
  assert.equal(await isSendPersisted(host({ ["/api/thread?id=" + id]: { peer, messages: [duplicate, duplicate] } }), rawEntry()), false);
});
