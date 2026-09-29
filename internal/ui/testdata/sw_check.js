// Checks the service worker (static/sw.js) with a stand-in worker scope:
// what a push shows, and where a click goes. Run by page_test.go.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

let failed = 0;
const check = (ok, what) => { if (!ok) { failed++; console.log("FAIL: " + what); } };

function load(windows) {
  const handlers = {};
  const shown = [], opened = [], posted = [], focused = [];
  const self = {
    location: new URL("https://relay.example/sw.js"),
    registration: { showNotification: async (title, opts) => { shown.push({ title, opts }); } },
    clients: {
      matchAll: async (q) => { check(q.type === "window" && q.includeUncontrolled, "every window is asked"); return windows; },
      openWindow: async (u) => { opened.push(u); },
    },
    addEventListener: (ev, f) => { handlers[ev] = f; },
  };
  for (const w of windows) {
    w.postMessage = (m) => posted.push({ url: w.url, m });
    w.focus = async () => { focused.push(w.url); };
  }
  const src = fs.readFileSync(path.join(__dirname, "../static/sw.js"), "utf8");
  vm.runInNewContext(src, { self, URL, console });
  const run = async (ev, e) => { let p; e.waitUntil = (x) => { p = x; }; handlers[ev](e); await p; };
  return { handlers, shown, opened, posted, focused, run };
}

const chan = "AbCdEfGhIjKlMnOpQrStUv";
(async () => {
  const sw = load([]);
  check(Object.keys(sw.handlers).sort().join(",") === "notificationclick,push", "only push and click are handled (no fetch, no install takeover)");
  const push = (data) => sw.run("push", { data: data === undefined ? null : { json: () => (typeof data === "string" ? JSON.parse(data) : data) } });
  await push({ v: 1, chan });
  await push({ v: 1, chan: "" });
  await push({ v: 1, chan: "../../evil?x=1" });
  await push({ v: 2, chan });
  await push("not json");
  await push(undefined);
  check(sw.shown.length === 6, "every push shows a notification");
  check(sw.shown.every((s) => s.title === "AgentNet" && s.opts.body === "New activity"), "content-free: AgentNet / New activity");
  check(sw.shown[0].opts.tag === chan && sw.shown[0].opts.data.chan === chan, "a channel groups its notifications");
  check(sw.shown.slice(1).every((s) => s.opts.tag === "summary" && s.opts.data.chan === ""), "anything else is a summary: " + JSON.stringify(sw.shown.map((s) => s.opts.tag)));

  // A click with an AgentNet window open: that window is told, and focused.
  const click = (s, data) => s.run("notificationclick", { notification: { data, close() {} } });
  const w1 = load([{ url: "https://relay.example/" }, { url: "https://other.example/" }]);
  await click(w1, { chan });
  check(w1.posted.length === 1 && w1.posted[0].url === "https://relay.example/" && w1.posted[0].m.type === "agentnet-open" && w1.posted[0].m.chan === chan,
    "the open AgentNet window is told the channel, no other origin: " + JSON.stringify(w1.posted));
  check(w1.focused.length === 1 && w1.opened.length === 0, "it is focused, no new window");
  // With none open: a new one, told only a valid channel, in its fragment.
  const w0 = load([]);
  await click(w0, { chan });
  await click(w0, { chan: "https://evil.example/" });
  await click(w0, null);
  check(w0.opened.join(" ") === "/#agentnet-open:" + chan + " /#agentnet-open: /#agentnet-open:",
    "a click opens this origin's page with the channel, never a link from the push: " + w0.opened.join(" "));
  if (failed) process.exit(1);
  console.log("service worker ok");
})();
