// Visibility only: the entry retains its read visit, attention and acknowledgement gates.
// Update with current mounted rows and canonical DTO IDs; destroy on view/visit replacement.
// Existing scroll/ResizeObserver callbacks call evaluate(); reserves read cached inset heights.
export function watchVisibleRows(root, {onChange, reserve = () => 0, reserveTop = () => 0, allowed = () => true}) {
  const document = root.ownerDocument, window = document.defaultView;
  let alive = true, epoch = 0, frame;
  let rows = new Map(), reported = new Set();
  const members = new Set();
  const publish = ids => {
    if (ids.size === reported.size && [...ids].every(id => reported.has(id))) return;
    reported = ids;
    onChange(new Set(ids));
  };
  const permitted = () => root.isConnected !== false && document.visibilityState === 'visible' && document.hasFocus() && allowed();
  const cancel = () => {
    ++epoch;
    if (frame !== undefined) window.cancelAnimationFrame(frame);
    frame = undefined;
  };
  const evaluate = () => {
    if (!alive) return;
    if (!permitted()) { cancel(); publish(new Set()); return; }
    if (frame !== undefined) return;
    const current = epoch;
    frame = window.requestAnimationFrame(() => {
      if (!alive || current !== epoch) return;
      frame = undefined;
      if (!permitted() || !members.size) { publish(new Set()); return; }
      const viewport = root.getBoundingClientRect();
      const top = Math.min(viewport.bottom, viewport.top + Math.max(0, reserveTop()));
      const bottom = Math.max(top, viewport.bottom - Math.max(0, reserve()));
      const height = bottom - top, ids = new Set();
      if (height > 0 && viewport.width > 0) for (const row of members) {
        const id = rows.get(row);
        if (!id || row.dataset.mid !== id || !root.contains(row)) continue;
        const r = row.getBoundingClientRect();
        const width = Math.min(r.right, viewport.right) - Math.max(r.left, viewport.left);
        const visible = Math.min(r.bottom, bottom) - Math.max(r.top, top);
        if (r.height > 0 && width > 0 && visible > 0 && visible >= Math.min(r.height / 2, height / 2)) ids.add(id);
      }
      publish(ids);
    });
  };
  const observer = new window.IntersectionObserver(entries => {
    if (!alive) return;
    for (const entry of entries) {
      const id = rows.get(entry.target);
      if (id && entry.target.dataset.mid === id && entry.isIntersecting && entry.intersectionRect.width > 0 && entry.intersectionRect.height > 0) members.add(entry.target);
      else members.delete(entry.target);
    }
    evaluate();
  }, {root, threshold: 0});
  const update = (nodes, canonicalIDs) => {
    if (!alive) return;
    const ids = new Set(canonicalIDs), next = new Map();
    for (const row of nodes) if (ids.has(row.dataset.mid) && root.contains(row)) next.set(row, row.dataset.mid);
    for (const [row, id] of rows) if (next.get(row) !== id) { observer.unobserve(row); members.delete(row); }
    for (const [row, id] of next) if (rows.get(row) !== id) observer.observe(row);
    rows = next;
    evaluate();
  };
  window.addEventListener('focus', evaluate);
  window.addEventListener('blur', evaluate);
  document.addEventListener('visibilitychange', evaluate);
  const destroy = () => {
    if (!alive) return;
    alive = false; cancel(); observer.disconnect(); rows.clear(); members.clear(); publish(new Set());
    window.removeEventListener('focus', evaluate);
    window.removeEventListener('blur', evaluate);
    document.removeEventListener('visibilitychange', evaluate);
  };
  return {update, evaluate, destroy};
}
