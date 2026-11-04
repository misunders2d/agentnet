// One browser-history guard delegates to the skin's existing layer dismissal.
// It never reads messages, authorizes work, or stores a conversation in history.
export function browserBackNavigation(win = window) {
  let handler, armed = false, leaving = false, canLeave = false;
  const oldMarker = win.history.state?.agentnetBack;
  const marker = typeof oldMarker === 'string' && oldMarker.startsWith('agentnet-back-') ? oldMarker : 'agentnet-back-' + win.crypto.randomUUID();
  // Each entry this page knows carries its place, so going back never depends on the
  // length of history, which stays the same at its cap or after Forward entries are cut.
  const own = state => state?.agentnetBack === marker && Number.isInteger(state.agentnetBackSeq);
  const stamp = seq => ({ ...win.history.state, agentnetBack: marker, agentnetBackSeq: seq });
  let at = own(win.history.state) ? win.history.state.agentnetBackSeq : -1;
  const guard = () => { at += 1; win.history.pushState(stamp(at), '', win.location.href); };
  // An older version's entry has no mark. Keep going past it; if nothing is earlier, stay able to handle Back.
  const leavePast = () => {
    win.history.back();
    win.setTimeout(() => {
      if (!leaving || own(win.history.state)) return;
      leaving = false; canLeave = false; at = 0;
      win.history.replaceState(stamp(at), '', win.location.href);
      guard();
    }, 1000);
  };
  const pop = event => {
    if (!own(event.state)) {
      if (leaving) { leavePast(); return; }
      // A link or notification changing the fragment made a new entry. It is not Back.
      at += 1; win.history.replaceState(stamp(at), '', win.location.href);
      return;
    }
    const back = event.state.agentnetBackSeq < at;
    at = event.state.agentnetBackSeq;
    if (!back || !handler || leaving) return;
    if (handler()) { if (at === 0) guard(); }
    else if (canLeave) { leaving = true; win.history.go(-(at + 1)); }
    else if (at === 0) guard(); // a directly opened tab has no earlier document to return to
  };
  const page = () => { leaving = false; if (own(win.history.state)) at = win.history.state.agentnetBackSeq; };
  win.addEventListener('popstate', pop);
  win.addEventListener('pageshow', page);
  return fn => {
    handler = fn;
    if (!armed) { canLeave = win.history.length > Math.max(at, 0) + 1; armed = true; }
    if (!own(win.history.state)) { at = Math.max(at, 0); win.history.replaceState(stamp(at), '', win.location.href); }
    if (at === 0) guard();
    return () => { if (handler === fn) handler = undefined; };
  };
}
