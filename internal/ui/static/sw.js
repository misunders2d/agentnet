// The browser device's service worker. It does two things: it shows a push
// from the relay as a content-free notification, and it opens the
// conversation the notification names when it is clicked. Nothing else:
// no fetch handler, no caches, no stream, no keys, no update takeover.
"use strict";

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
