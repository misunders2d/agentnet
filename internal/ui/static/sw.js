// The browser device's service worker. It does two things: it shows a push
// from the relay as a content-free notification, and it opens the
// conversation the notification names when it is clicked. Imported local UI
// packages also have a reserved fetch path; API/relay traffic is untouched.
// No stream, keys or automatic update takeover. Local interface selection is
// the only page operation that asks this worker to activate/claim a page.
"use strict";

self.addEventListener("message", event => {
  let origin;
  try { origin = new URL(event.source?.url).origin; } catch (_) { return; }
  if (origin !== self.location.origin) return;
  if (event.data?.type === "agentnet-local-skins-activate") event.waitUntil(self.skipWaiting());
  if (event.data?.type === "agentnet-local-skins-claim") event.waitUntil(self.clients.claim().then(() => event.ports[0]?.postMessage({ ready: true })));
});
self.addEventListener("fetch", event => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin || !url.pathname.startsWith("/local-skins/")) return;
  // Only an immutable imported snapshot's declared files. Never network
  // fallback: a removed/partial package cannot become server-supplied code.
  event.respondWith((async () => {
    const match = /^\/local-skins\/([0-9a-f]{64})\/(.+)$/.exec(url.pathname);
    if (event.request.method !== "GET" || !match || url.search || /[\\%:]/.test(match[2]) || match[2].split("/").some(x => !x || x === "." || x === "..")) return new Response("Not found", { status: 404 });
    const name = "agentnet-skin-package-v1-" + match[1];
    if (!await caches.has(name)) return new Response("Not found", { status: 404 });
    const cache = await caches.open(name);
    if (!await cache.match(new URL("/local-skins/ready", self.location.origin))) return new Response("Not found", { status: 404 });
    return await cache.match(event.request) || new Response("Not found", { status: 404 });
  })());
});

// A channel is 22 base64url characters (docs/revival/NOTIFY.md §2); any
// other value, or none, is a summary of several conversations.
const channelPattern = /^[A-Za-z0-9_-]{22}$/;
const channelOf = (v) => (typeof v === "string" && channelPattern.test(v) ? v : "");

self.addEventListener("push", (event) => {
  let chan = "";
  try {
    const p = event.data ? event.data.json() : null;
    if (p && p.v === 1) chan = channelOf(p.chan);
  } catch (e) { /* unreadable: shown as a summary */ }
  // Every push is shown (a browser may require it), and says nothing of
  // the message: the page reads it after decrypting it.
  event.waitUntil(self.registration.showNotification("AgentNet", {
    body: "New activity", tag: chan || "summary", renotify: true, icon: "/assets/icon-192.png", data: { chan },
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const chan = channelOf(event.notification.data && event.notification.data.chan);
  event.waitUntil((async () => {
    // The open AgentNet window resolves the channel itself; nothing from
    // the push is followed as a link.
    const wins = (await self.clients.matchAll({ type: "window", includeUncontrolled: true }))
      .filter((w) => new URL(w.url).origin === self.location.origin);
    if (wins.length) {
      for (const w of wins) w.postMessage({ type: "agentnet-open", chan });
      await wins[0].focus();
      return;
    }
    await self.clients.openWindow("/#agentnet-open:" + chan);
  })());
});
