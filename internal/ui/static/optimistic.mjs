// UI-only send previews. Durable messages and all authority remain the host's.
const ledgers = new WeakMap();
const ledgerKey = Symbol.for("agentnet.ui.pendingSends");
export function sendID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('');
}
// One composer gesture, with independent request rows and actions. Only explicit
// signed grouping from the same exact author is eligible. Legacy, changed or
// inconsistent rows stay fully visible; matching text never creates a group.
export function groupedSends(messages) {
  const buckets = new Map(), keys = new Map();
  for (const m of messages) {
    if (!/^[a-f0-9]{32}$/.test(m.send_group || '') || !m.send_group_author) continue;
    const key = JSON.stringify([m.send_group_author, m.send_group, m.topic || '']);
    if (!buckets.has(key)) buckets.set(key, []);
    buckets.get(key).push(m); keys.set(m, key);
  }
  const eligible = m => ['question','task'].includes(m.kind) && m.origin === 'ui' && m.pid && !m.agent_id && !m.event && !m.excerpt_pid && !m.deleted && !m.edited && !m.proposal;
  const content = m => JSON.stringify([m.kind, m.body || '', m.quote || '', (m.attachments || m.files || []).map(f => [f.name, f.size])]);
  for (const [key, rows] of buckets) {
    if (rows.length < 2 || !rows.every(eligible) || rows.some(m => content(m) !== content(rows[0])) || new Set(rows.map(m => m.pid)).size !== rows.length) buckets.delete(key);
  }
  const out = [], emitted = new Set();
  for (const m of messages) {
    const key = keys.get(m), rows = buckets.get(key);
    if (!rows) { out.push({m, compact:false}); continue; }
    if (emitted.has(key)) continue;
    emitted.add(key);
    rows.forEach((m, i) => out.push({m, compact:i > 0}));
  }
  return out;
}
export function pendingSends(host, changed = () => {}) {
  const state = host.workspaces?.state?.(host.workspace.id);
  // Package-local copies share previews through the public membership state.
  let shared = state ? state[ledgerKey] : ledgers.get(host);
  if (!shared) {
    shared = {ledger: new Map(), changed};
    if (state) state[ledgerKey] = shared; else ledgers.set(host, shared);
  }
  shared.changed = changed; // the currently mounted renderer owns notifications
  const ledger = shared.ledger, notify = () => shared.changed?.();
  const rows = key => [...ledger.values()].filter(x => x.key === key);
  return {
    begin(key, message, retry) {
      const m = { ...message, _local: true, _retry: retry, state: 'queued', delivery: 'queued', state_text: 'Sending…', can: [], actions: [], copies: [], reactions: [] };
      const previous = rows(key).at(-1);
      let saved; const stored = new Promise(resolve => { saved = resolve; });
      ledger.set(m.id, {key, m, ready: previous?.stored || Promise.resolve(), stored, saved}); notify(); return m.id;
    },
    has: id => ledger.has(id),
    move(id, key) { const x = ledger.get(id); if (x) { x.key = key; notify(); } },
    ready: id => ledger.get(id)?.ready || Promise.resolve(),
    finish(id, result) {
      const x = ledger.get(id); if (!x) return;
      x.saved(); x.done = true;
      x.m.id = result?.id || id;
      x.m.lid = result?.lid || x.m.lid;
      x.m.state = result?.state || 'queued'; x.m.delivery = x.m.state;
      x.m.state_text = ['custody','delivered'].includes(x.m.state) ? (x.m.state === 'custody' ? 'On your server' : 'Delivered') : result?.detail || 'Sending…';
      notify();
    },
    fail(id, reason) {
      const x = ledger.get(id); if (!x) return false;
      x.saved();
      x.m.state = x.m.delivery = 'failed'; x.m.state_text = 'Not sent: ' + reason;
      x.m._failed = true; notify(); return true;
    },
    remove(id) { ledger.get(id)?.saved(); ledger.delete(id); notify(); },
    // unsent: a send begun here and not yet stored, or failed and kept with
    // its Retry. Its draft was cleared as it began: a reload would lose it.
    unsent: () => [...ledger.values()].some(x => !x.done),
    dispose() { if (shared.changed === changed) shared.changed = null; },
    merge(key, messages = []) {
      const actual = messages.filter(m => !m._local);
      for (const [id, x] of ledger) if (x.key === key && actual.some(m => m.dir === 'out' && (m.id === x.m.id || (m.lid && m.lid === x.m.lid)))) { x.saved(); ledger.delete(id); }
      return [...actual, ...rows(key).map(x => x.m)];
    },
  };
}
