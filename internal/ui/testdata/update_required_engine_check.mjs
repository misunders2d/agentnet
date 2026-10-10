// A browser page whose code is older than its server requires or serves
// (owner policy "latest only", v0.8.17): a 426 update_required refusal or
// the stream's update_required event marks it outdated once, after which
// the engine sends its server only what the server still takes from a
// refused device (its stream, ping acks, the version probe and the
// recommendation; receipts, member lookups, answers and results), so
// nothing loops and admitted work drains; a refusal has the server's
// version asked again; a /v1/version naming another build than the page was
// served as marks it stale, and the overview then names that build as its
// version, so the skin reloads as for any new version (never over unsent
// drafts), at most once per build and target in a tab; the server serving
// it again (members) ends a refusal; the overview carries the banner's field.
import assert from "node:assert/strict";
import { Engine, memoryStore, HubError, updateReloadDue } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";

const refusal = JSON.stringify({ error: "update_required", latest: "v0.8.18", url: "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18", message: "Update AgentNet to v0.8.18 to continue." });
const tabStorage = () => { const mem = new Map(); return { getItem: (k) => mem.has(k) ? mem.get(k) : null, setItem: (k, v) => mem.set(k, String(v)) }; };
const served = new Set(["GET /v1/version", "GET /v1/release", "POST /v1/stream/ack", "POST /v1/messages/m1/ack", "GET /v1/agents/admin/alice", "GET /v1/agents/admin/alice/sessions", "GET /v1/agents/admin/alice/profile"]);
const engine = async (pageBuild, version = () => "v0.8.17", tab = tabStorage()) => {
  const calls = [];
  const fetch = async (url, opts) => {
    const p = new URL(url).pathname, m = (opts && opts.method) || "GET";
    calls.push(m + " " + p);
    if (p === "/v1/version") return new Response(JSON.stringify({ version: version(), protocol: 1, features: [] }));
    if (served.has(m + " " + p)) return new Response("{}");
    if (m === "POST" && p === "/v1/messages" && ["answer", "result"].includes(JSON.parse(opts.body).kind)) return new Response(JSON.stringify({ state: "custody" }));
    return new Response(refusal, { status: 426, headers: { "Content-Type": "application/json" } });
  };
  const e = new Engine({ store: memoryStore(), base: "https://relay.example", fetch, pageBuild, tab });
  e.keys = await wire.newKeys(); e.address = "self/phone";
  e.fp = await wire.fingerprint(await wire.publicEntry(e.keys, e.address));
  let changes = 0;
  const changed = e.changed.bind(e);
  e.changed = () => { changes++; return changed(); };
  return { e, calls, changes: () => changes };
};
const settle = () => new Promise((r) => setTimeout(r, 20));

// 1. A refusal: once refused, nothing reaches the server but what it still
// takes from a refused device; the request is a retryable 426, not a failure.
{
  const { e, calls, changes } = await engine("v0.8.17");
  await e.features();
  await assert.rejects(e.call("POST", "/v1/messages", { id: "x", kind: "message" }), (err) => err instanceof HubError && err.status === 426 && err.code === "update_required" && /v0\.8\.18/.test(err.message));
  assert.equal(calls.filter((c) => c === "POST /v1/messages").length, 1);
  assert.deepEqual([e.outdated.refused, e.outdated.latest, e.outdated.url], [true, "v0.8.18", "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18"]);
  await settle();
  assert.equal(calls.filter((c) => c === "GET /v1/version").length, 2, "a refusal asks the server's version once more");
  const told = changes();
  for (let i = 0; i < 5; i++) {
    await assert.rejects(e.call("POST", "/v1/messages", { id: "x" + i, kind: "question" }), (err) => err.status === 426);
    await assert.rejects(e.call("PUT", "/v1/caps", {}), (err) => err.status === 426);
    await assert.rejects(e.getBytes("/v1/blobs/" + "a".repeat(32)), (err) => err.status === 426);
  }
  assert.equal(calls.filter((c) => !served.has(c)).length, 1, "no request loop: refused here, not at the server: " + calls.join(", "));
  // What lets admitted work drain still goes.
  const before = calls.length;
  await e.call("GET", "/v1/version", undefined, { signed: false });
  await e.call("POST", "/v1/stream/ack", { conn: "c" });
  await e.call("POST", "/v1/messages/m1/ack", { state: "delivered" });
  await e.call("GET", "/v1/release");
  await e.call("GET", "/v1/agents/admin/alice");
  await e.call("GET", "/v1/agents/admin/alice/sessions");
  await e.call("GET", "/v1/agents/admin/alice/profile");
  assert.deepEqual(await e.call("POST", "/v1/messages", { id: "a", kind: "answer" }), { state: "custody" });
  assert.deepEqual(await e.call("POST", "/v1/messages", { id: "r", kind: "result" }), { state: "custody" });
  assert.equal(calls.length - before, 9, "all of them reached the server: " + calls.slice(before).join(", "));
  assert.equal(changes(), told, "the same refusal is not told again");
  assert.ok(e.outdated.refused, "what the server still takes does not end the refusal");
  // The server serves this very build: reloading cannot help; the banner says so.
  const o = await e.overview();
  assert.equal(o.version, "v0.8.17", "no new version: the skin keeps this page");
  assert.deepEqual(o.update_required, { latest: "v0.8.18", url: "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18", auto: "Your server does not offer it to this page yet: ask its admin to update the server." });
  // The server serves this device again: its member list ends the refusal.
  await e.dispatch("members", JSON.stringify({ members: [{ address: "self/phone", presence: "connected", joined: 1 }], truncated: false }));
  assert.equal(e.outdated, null);
  assert.equal((await e.overview()).update_required, undefined);
  e.stop();
}

// 2. The stream's update_required event refuses without any request but
// the version probe, which finds the server serving a newer build: the
// overview names it, so the skin reloads the page (as for any new version).
{
  let v = "v0.8.17";
  const { e, calls } = await engine("v0.8.17", () => v);
  await e.features(); calls.length = 0;
  v = "v0.8.19"; // the server was updated meanwhile
  await e.dispatch("update_required", JSON.stringify({ latest: "v0.8.19", url: "https://example.test/r" }));
  assert.deepEqual([e.outdated.refused, e.outdated.latest], [true, "v0.8.19"]);
  await assert.rejects(e.call("POST", "/v1/messages", {}), (err) => err.status === 426);
  await settle();
  assert.deepEqual(calls, ["GET /v1/version"], "refused here; the version asked once");
  const o = await e.overview();
  assert.deepEqual([o.version, o.update_required.latest, o.update_required.auto], ["v0.8.19", "v0.8.19", ""]);
  // What it names is shown only as a release tag and an https page.
  await e.dispatch("update_required", JSON.stringify({ latest: "evil text", url: "javascript:alert(1)" }));
  assert.deepEqual([e.outdated.latest, e.outdated.url], ["v0.8.19", "https://example.test/r"]);
  e.stop();
}

// 3. A server serving another build than this page was served as: stale;
// the overview names the server's build (the skin's reload), once per build
// and target in this tab: the same old page served again cannot help.
{
  let v = "v0.8.17";
  const tab = tabStorage();
  const { e } = await engine("v0.8.17", () => v, tab);
  await e.features();
  assert.equal(e.outdated, null, "the same build");
  assert.equal((await e.overview()).version, "v0.8.17");
  e.featureList = null; v = "v0.8.18"; // the server was updated; the next connection asks again
  await e.features();
  assert.deepEqual([e.outdated.stale, e.outdated.refused, e.outdated.latest, e.outdated.reload], [true, false, "v0.8.18", true]);
  const o = await e.overview();
  assert.equal(o.version, "v0.8.18", "the skin sees a new version");
  assert.equal(o.update_required, undefined, "no refusal: the page just reloads");
  e.stop();
  // The reloaded tab got the same old page: no second reload.
  const again = await engine("v0.8.17", () => "v0.8.18", tab);
  await again.e.features();
  assert.deepEqual([again.e.outdated.stale, again.e.outdated.reload], [true, false]);
  assert.equal((await again.e.overview()).version, "v0.8.17", "no reload loop");
  again.e.stop();
}

// 4. Without a stamp, the first answer is the page's build.
{
  let v = "v0.8.17";
  const { e } = await engine("", () => v);
  await e.features();
  assert.deepEqual([e.pageBuild, e.outdated], ["v0.8.17", null]);
  e.featureList = null; v = "v0.8.18";
  await e.features();
  assert.equal(e.outdated.stale, true);
  assert.equal((await e.overview()).version, "v0.8.18");
  e.stop();
}

// 5. Another workspace's engine (no page build) never calls its server's
// build stale: this page's files come from its own server.
{
  let v = "v0.8.17";
  const { e } = await engine(undefined, () => v);
  await e.features(); e.featureList = null; v = "v0.8.18"; await e.features();
  assert.equal(e.outdated, null);
  e.stop();
}

// 6. One reload per build and target; no working storage, no reload.
{
  const tab = tabStorage();
  assert.equal(updateReloadDue(tab, "v0.8.17", "v0.8.18"), true);
  assert.equal(updateReloadDue(tab, "v0.8.17", "v0.8.18"), false, "reloaded once already: the server had nothing newer");
  assert.equal(updateReloadDue(tab, "v0.8.18", "v0.8.19"), true, "a newer page, a newer target");
  assert.equal(updateReloadDue({ getItem() { throw new Error("blocked"); }, setItem() {} }, "a", "b"), false);
  assert.equal(updateReloadDue(null, "a", "b"), false);
  assert.equal(updateReloadDue({ getItem: () => null, setItem() {} }, "a", "b"), false, "storage that keeps nothing cannot stop a loop");
}
console.log("update required engine check: ok");
