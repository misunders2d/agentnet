// UI-only send previews. Durable messages and all authority remain the host's.
const ledgers = new WeakMap();
export function sendID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join('');
}
export function pendingSends(host, changed = () => {}) {
  const scope = host.workspaces?.state?.(host.workspace.id) || host;
  let shared = ledgers.get(scope);
  if (!shared) { shared = {ledger: new Map(), changed}; ledgers.set(scope, shared); }
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
      x.saved();
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
    dispose() { if (shared.changed === changed) shared.changed = null; },
    merge(key, messages = []) {
      const actual = messages.filter(m => !m._local);
      for (const [id, x] of ledger) if (x.key === key && actual.some(m => m.dir === 'out' && (m.id === x.m.id || (m.lid && m.lid === x.m.lid)))) { x.saved(); ledger.delete(id); }
      return [...actual, ...rows(key).map(x => x.m)];
    },
  };
}
