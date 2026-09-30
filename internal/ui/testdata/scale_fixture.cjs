// Synthetic workspace for navigation regressions and local visual checks.
// No identities, keys or messages from a real installation.
module.exports = function scaleFixture(base) {
  const o = JSON.parse(JSON.stringify(base));
  o.people = []; o.dms = []; o.threads = []; o.review = []; o.links = []; o.reminders = [];
  o.directory = { status: "listed", current: true, at: "2026-09-30T09:00:00Z", members: [] };
  const conversations = {};
  for (let i = 0; i < 50; i++) {
    const n = String(i).padStart(2, "0"), devices = ["laptop", "phone", "tablet"].map((name) => ({ name, address: "person" + n + "/" + name, fingerprint: "synthetic-" + n + "-" + name }));
    const p = { person: "person-" + n, label: "Person " + n, address: devices[0].address, state: "pinned", devices };
    o.people.push(p);
    o.directory.members.push(...devices.map((d) => ({ address: d.address, presence: "connected" })));
    for (let j = 0; j < 6; j++) {
      const id = "scale-" + n + "-" + j, at = new Date(Date.UTC(2026, 8, 1) + (i * 6 + j) * 3600e3).toISOString();
      const title = j === 0 ? "Weekend plans " + n : "Project " + n + " discussion " + j;
      o.dms.push({ id, peer: p, title, last: "Latest note in " + title, last_at: at, created: at, count: 2, mine: true,
        unread: i % 5 === 0 && j === 5 ? 1 : 0, held: 0, waiting: 0 });
      conversations[id] = { id, peer: p, created: at, mine: true, messages: [{ id: id + "-m", lid: id + "-m", dir: "in", from: p.address,
        body: "Synthetic chat: " + title, at, kind: "message", attachments: [], author: { label: p.label } }] };
    }
  }
  for (let i = 0; i < 30; i++) {
    const n = String(i).padStart(2, "0"), peer = "service" + n + "/bot";
    o.directory.members.push({ address: peer, presence: "connected" });
    for (let j = 0; j < 2; j++) o.threads.push({ id: "service-" + n + "-" + j, peer, title: "Service check " + j, last: "Check complete",
      last_at: new Date(Date.UTC(2026, 8, 20) + i * 3600e3).toISOString(), count: 2, unread: i % 6 === 0 && j === 0 ? 1 : 0,
      review: 0, running: 0, notices: 0, waiting: false });
  }
  return { overview: o, conversations };
};
