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
  // The person's Retry or Discard on a message the host keeps: shown at once,
  // undone if the host refuses, cleared when its own state shows the change.
  const actions = shared.actions ||= new Map();
  const act = (kind, m, key) => {
    actions.set(m.id, {kind, rev: m.queued_send.rev, key});
    notify();
    return host.api('/api/schedule/' + (kind === 'retry' ? 'retry_send' : 'discard_send'), {id: m.id}).catch(e => { actions.delete(m.id); notify(); throw e; });
  };
  const queuedRow = (m, key) => {
    const action = actions.get(m.id), stopped = m.queued_send.state === 'needs_review' && action?.kind !== 'retry';
    const waiting = !stopped && !action && m.queued_send.state === 'pending' && m.detail;
    return {...m, _local: true, _failed: stopped, delivery: stopped ? 'failed' : 'queued', state_text: stopped ? m.detail || 'Not sent.' : waiting || 'Sending…',
      _retry: () => act('retry', m, key), _discard: () => act('discard', m, key), _diagnostic: m.queued_send.diagnostic,
      can: [], actions: [], copies: [], reactions: []};
  };
  const when = m => { const t = Date.parse(m.at); return Number.isNaN(t) ? Infinity : t; };
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
      const state = result?.state || 'queued';
      x.m = { ...x.m, id: result?.id || id, lid: result?.lid || x.m.lid, state, delivery: state,
        state_text: ['custody','delivered'].includes(state) ? (state === 'custody' ? 'On your server' : 'Delivered') : result?.detail || 'Sending…' };
      notify();
    },
    fail(id, reason, unsure) {
      const x = ledger.get(id); if (!x) return false;
      x.saved();
      x.m = { ...x.m, state: 'failed', delivery: 'failed', state_text: (unsure ? 'Not confirmed: ' : 'Not sent: ') + reason, _failed: true,
        _discard: () => { ledger.get(id)?.saved(); ledger.delete(id); notify(); } };
      notify(); return true;
    },
    remove(id) { ledger.get(id)?.saved(); ledger.delete(id); notify(); },
    // unsent: a send begun here and not yet stored, or failed and kept with
    // its Retry. Its draft was cleared as it began: a reload would lose it.
    unsent: () => [...ledger.values()].some(x => !x.done),
    dispose() { if (shared.changed === changed) shared.changed = null; },
    merge(key, messages = []) {
      const actual = messages.some(m => m._local && !m.queued_send) ? messages.filter(m => !m._local || m.queued_send) : messages;
      for (const [id, x] of ledger) if (x.key === key && actual.some(m => m.dir === 'out' && (m.id === x.m.id || (m.lid && m.lid === x.m.lid)))) { x.saved(); ledger.delete(id); }
      for (const [id, action] of actions) {
        if (action.key !== key) continue;
        const row = actual.find(m => m.id === id && m.queued_send);
        if (!row || action.kind === 'retry' && row.queued_send.rev !== action.rev) actions.delete(id);
      }
      const shown = actual.some(m => m.queued_send) ? actual.filter(m => !m.queued_send || actions.get(m.id)?.kind !== 'discard').map(m => m.queued_send ? queuedRow(m, key) : m) : actual;
      const pending = rows(key);
      if (!pending.length) return shown;
      // A yet-unsent message keeps the place it took, and a failed one keeps it
      // too: later arrivals go below it (TDLib 4c6377aa, MessagesManager.cpp:
      // get_next_yet_unsent_message_id, fail_send_message).
      const out = [...shown];
      for (const {m} of pending) { let i = out.length; while (i > 0 && when(out[i - 1]) > when(m)) i--; out.splice(i, 0, m); }
      return out;
    },
  };
}
