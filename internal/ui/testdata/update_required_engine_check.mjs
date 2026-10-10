// A browser page whose code is older than its server requires or serves
// (owner policy "latest only", v0.8.17): a 426 update_required refusal or
// the stream's update_required event marks it outdated once, after which
// the engine sends its server nothing but its stream, ping acks and the
// version probe (no request loops); a /v1/version naming another build than
// the page was served as marks it stale; the page is told (onOutdated) to
// reload, at most once per build and target; the server serving it again
// (members) ends a refusal; the overview carries the banner's field.
import assert from "node:assert/strict";
import { Engine, memoryStore, HubError, updateReloadDue } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";

const refusal = JSON.stringify({ error: "update_required", latest: "v0.8.18", url: "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18", message: "Update AgentNet to v0.8.18 to continue." });
const engine = async (pageBuild, version = () => "v0.8.17") => {
  const calls = [];
  const fetch = async (url, opts) => {
    const p = new URL(url).pathname, m = (opts && opts.method) || "GET";
    calls.push(m + " " + p);
    if (p === "/v1/version") return new Response(JSON.stringify({ version: version(), protocol: 1, features: [] }));
    if (p === "/v1/stream/ack") return new Response("{}");
    return new Response(refusal, { status: 426, headers: { "Content-Type": "application/json" } });
  };
  const e = new Engine({ store: memoryStore(), base: "https://relay.example", fetch, pageBuild });
  e.keys = await wire.newKeys(); e.address = "self/phone";
  e.fp = await wire.fingerprint(await wire.publicEntry(e.keys, e.address));
  const told = [];
  e.onOutdated = (o) => told.push({ ...o });
  return { e, calls, told };
};

// 1. A refusal: once refused, nothing more reaches the server but the
// allowed requests; the request is a retryable 426, not a failure.
{
  const { e, calls, told } = await engine("v0.8.17");
  await assert.rejects(e.call("POST", "/v1/messages", { id: "x" }), (err) => err instanceof HubError && err.status === 426 && err.code === "update_required" && /v0\.8\.18/.test(err.message));
  assert.equal(calls.filter((c) => c === "POST /v1/messages").length, 1);
  assert.deepEqual([e.outdated.refused, e.outdated.latest, e.outdated.url], [true, "v0.8.18", "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18"]);
  assert.equal(told.length, 1, "the page is told once");
  for (let i = 0; i < 5; i++) {
    await assert.rejects(e.call("POST", "/v1/messages", { id: "x" + i }), (err) => err.status === 426);
    await assert.rejects(e.call("PUT", "/v1/caps", {}), (err) => err.status === 426);
    await assert.rejects(e.getBytes("/v1/blobs/" + "a".repeat(32)), (err) => err.status === 426);
  }
  assert.equal(calls.filter((c) => c !== "GET /v1/version" && c !== "POST /v1/stream/ack").length, 1, "no request loop: refused here, not at the server: " + calls.join(", "));
  await e.call("GET", "/v1/version", undefined, { signed: false });
  await e.call("POST", "/v1/stream/ack", { conn: "c" });
  assert.deepEqual(calls.slice(-2), ["GET /v1/version", "POST /v1/stream/ack"], "the probe and ping acks still go");
  assert.equal(told.length, 1, "the same refusal is not told again");
  const o = await e.overview();
  assert.deepEqual(o.update_required, { latest: "v0.8.18", url: "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18", auto: "" });
  e.reloadDeclined();
  assert.match((await e.overview()).update_required.auto, /does not offer it to this page yet/);
  // The server serves this device again: its member list ends the refusal.
  await e.dispatch("members", JSON.stringify({ members: [{ address: "self/phone", presence: "connected", joined: 1 }], truncated: false }));
  assert.equal(e.outdated, null);
  assert.equal((await e.overview()).update_required, undefined);
  e.stop();
}

// 2. The stream's update_required event refuses without any request.
{
  const { e, calls, told } = await engine("v0.8.17");
  await e.dispatch("update_required", JSON.stringify({ latest: "v0.8.19", url: "https://example.test/r" }));
  assert.deepEqual([e.outdated.refused, e.outdated.latest, told.length], [true, "v0.8.19", 1]);
  await assert.rejects(e.call("POST", "/v1/messages", {}), (err) => err.status === 426);
  assert.equal(calls.length, 0, "refused here");
  // What it names is shown only as a release tag and an https page.
  await e.dispatch("update_required", JSON.stringify({ latest: "evil text", url: "javascript:alert(1)" }));
  assert.deepEqual([e.outdated.latest, e.outdated.url], ["v0.8.19", "https://example.test/r"]);
  e.stop();
}

// 3. A server serving another build than this page was served as: stale.
{
  let v = "v0.8.17";
  const { e, told } = await engine("v0.8.17", () => v);
  await e.features();
  assert.equal(e.outdated, null, "the same build");
  e.featureList = null; v = "v0.8.18"; // the server was updated; the next connection asks again
  await e.features();
  assert.deepEqual([e.outdated.stale, e.outdated.refused, e.outdated.latest, told.length], [true, false, "v0.8.18", 1]);
  assert.equal((await e.overview()).update_required, undefined, "no refusal: the page just reloads");
  e.stop();
}

// 4. Without a stamp, the first answer is the page's build.
{
  let v = "v0.8.17";
  const { e, told } = await engine("", () => v);
  await e.features();
  assert.deepEqual([e.pageBuild, e.outdated, told.length], ["v0.8.17", null, 0]);
  e.featureList = null; v = "v0.8.18";
  await e.features();
  assert.equal(e.outdated.stale, true);
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
  const mem = new Map(), tab = { getItem: (k) => mem.has(k) ? mem.get(k) : null, setItem: (k, v) => mem.set(k, String(v)) };
  assert.equal(updateReloadDue(tab, "v0.8.17", "v0.8.18"), true);
  assert.equal(updateReloadDue(tab, "v0.8.17", "v0.8.18"), false, "reloaded once already: the server had nothing newer");
  assert.equal(updateReloadDue(tab, "v0.8.18", "v0.8.19"), true, "a newer page, a newer target");
  assert.equal(updateReloadDue({ getItem() { throw new Error("blocked"); }, setItem() {} }, "a", "b"), false);
  assert.equal(updateReloadDue(null, "a", "b"), false);
  assert.equal(updateReloadDue({ getItem: () => null, setItem() {} }, "a", "b"), false, "storage that keeps nothing cannot stop a loop");
}
console.log("update required engine check: ok");
