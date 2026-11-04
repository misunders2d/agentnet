// One browser-history guard delegates to the skin's existing layer dismissal.
// It never reads messages, authorizes work, or stores a conversation in history.
export function browserBackNavigation(win = window) {
  let handler, armed = false, leaving = false, canLeave = false, giveUp;
  const oldMarker = win.history.state?.agentnetBack;
  const marker = typeof oldMarker === 'string' && oldMarker.startsWith('agentnet-back-') ? oldMarker : 'agentnet-back-' + win.crypto.randomUUID();
  // Each entry this page knows carries its place, so going back never depends on the
  // length of history, which stays the same at its cap or after Forward entries are cut.
  const own = state => state?.agentnetBack === marker && Number.isInteger(state.agentnetBackSeq);
  const stamp = seq => ({ ...win.history.state, agentnetBack: marker, agentnetBackSeq: seq });
  let at = own(win.history.state) ? win.history.state.agentnetBackSeq : -1;
  const guard = () => { at += 1; win.history.pushState(stamp(at), '', win.location.href); };
  // Leaving walks back one entry at a time. Places count entries the browser may have
  // dropped or a writer re-marked, so a distance worked out from them can point past the
  // start of history, where go() does nothing at all. An older version's entry has no
  // mark, and this walks past that as well.
  const leaveStep = () => {
    win.clearTimeout(giveUp);
    giveUp = win.setTimeout(nothingBehind, 1000);
    win.history.back();
  };
  // No traversal came: nothing older was kept. History stays as it is, because a slow
  // traversal may still be on its way; Back's guard returns with the next input.
  const nothingBehind = () => {
    leaving = false;
    canLeave = false;
    win.addEventListener('pointerdown', guardAgain, true);
    win.addEventListener('keydown', guardAgain, true);
  };
  const guardAgain = () => {
    win.removeEventListener('pointerdown', guardAgain, true);
    win.removeEventListener('keydown', guardAgain, true);
    if (leaving) return;
    at = 0;
    win.history.replaceState(stamp(at), '', win.location.href);
    guard();
  };
  const pop = event => {
    if (leaving) {
      if (own(event.state)) at = event.state.agentnetBackSeq;
      leaveStep();
      return;
    }
    if (!own(event.state)) {
      // A link or notification changing the fragment made a new entry. It is not Back.
      at += 1; win.history.replaceState(stamp(at), '', win.location.href);
      return;
    }
    const back = event.state.agentnetBackSeq < at;
    at = event.state.agentnetBackSeq;
    if (!back || !handler) return;
    if (handler()) { if (at === 0) guard(); }
    else if (canLeave) { leaving = true; leaveStep(); }
    else if (at === 0) guard(); // a directly opened tab has no earlier document to return to
  };
  const page = () => { leaving = false; win.clearTimeout(giveUp); if (own(win.history.state)) at = win.history.state.agentnetBackSeq; };
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
