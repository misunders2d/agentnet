// One browser-history guard delegates to the skin's existing layer dismissal.
// It never reads messages, authorizes work, or stores a conversation in history.
export function browserBackNavigation(win = window) {
  let handler, armed = false, walking = false, unanswered = false, returning = false, canLeave = false, giveUp;
  const nav = win.navigation;
  const oldMarker = win.history.state?.agentnetBack;
  const marker = typeof oldMarker === 'string' && oldMarker.startsWith('agentnet-back-') ? oldMarker : 'agentnet-back-' + win.crypto.randomUUID();
  // Each entry this page knows carries its place, so going back never depends on the
  // length of history, which stays the same at its cap or after Forward entries are cut.
  // The generation tells a guard written now from entries a late traversal lands on.
  const own = state => state?.agentnetBack === marker && Number.isInteger(state.agentnetBackSeq);
  const generationOf = state => Number.isInteger(state.agentnetBackGeneration) ? state.agentnetBackGeneration : 0;
  let generation = own(win.history.state) ? generationOf(win.history.state) : 0;
  const stamp = seq => ({ ...win.history.state, agentnetBack: marker, agentnetBackSeq: seq, agentnetBackGeneration: generation });
  let at = own(win.history.state) ? win.history.state.agentnetBackSeq : -1;
  const guard = () => { at += 1; win.history.pushState(stamp(at), '', win.location.href); };
  const guardFromHere = () => {
    generation += 1;
    at = 0;
    win.history.replaceState(stamp(at), '', win.location.href);
    guard();
  };
  // What the browser can say about the entry behind this one (HTML navigation API).
  // It lists only same-origin entries touching this one. History longer than that list
  // holds other origins: behind this page when it was pushed or replaced, since those cut
  // whatever was ahead of it, and on either side after a reload or a return.
  const behind = () => {
    const current = nav?.currentEntry;
    if (!current || !(current.index >= 0)) return 'unknown';
    if (current.index > 0) return 'own';
    if (win.history.length <= nav.entries().length) return 'nothing';
    const type = nav.activation?.navigationType;
    return type === 'push' || type === 'replace' ? 'other' : 'unknown';
  };
  const listen = on => {
    const change = on ? win.addEventListener : win.removeEventListener;
    change.call(win, 'pointerdown', inputArrived, true);
    change.call(win, 'keydown', inputArrived, true);
  };
  // Only an event from the browser ends a Back this page began, however late it comes.
  // Input may bring the guard back only once the browser cannot be expected to answer.
  const mayReturnOnInput = () => { returning = true; if (handler) listen(true); };
  const inputArrived = () => {
    listen(false);
    returning = false;
    unanswered = walking;
    walking = false;
    guardFromHere();
  };
  // Leaving walks back one entry at a time. Places count entries the browser may have
  // dropped or a writer re-marked, so a distance worked out from them can point past the
  // start of history, where going back does nothing at all, and no event says so. An
  // older version's entry has no mark, and this walks past that as well.
  const leaveStep = () => {
    win.clearTimeout(giveUp);
    const there = behind();
    if (there === 'nothing') { walking = false; canLeave = false; guardFromHere(); return; }
    walking = true;
    if (there === 'own') {
      const refused = () => { if (walking) { walking = false; mayReturnOnInput(); } };
      const step = nav.back();
      step.committed?.catch(refused);
      step.finished?.catch(() => {});
      return;
    }
    // The browser cannot say whether anything is behind this entry, and going back past
    // the start raises no event, so a slow traversal looks the same as none at all.
    if (there === 'unknown') giveUp = win.setTimeout(mayReturnOnInput, 1000);
    win.history.back();
  };
  const pop = event => {
    const late = unanswered && own(event.state) && generationOf(event.state) < generation;
    unanswered = false;
    if (walking || late) {
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
    else if (canLeave) leaveStep();
    else if (at === 0) guard(); // a directly opened tab has no earlier document to return to
  };
  const page = () => {
    walking = false; unanswered = false; returning = false;
    win.clearTimeout(giveUp);
    listen(false);
    if (own(win.history.state)) at = win.history.state.agentnetBackSeq;
  };
  win.addEventListener('popstate', pop);
  win.addEventListener('pageshow', page);
  return fn => {
    handler = fn;
    if (!armed) { canLeave = win.history.length > Math.max(at, 0) + 1; armed = true; }
    if (returning) listen(true);
    if (!walking) {
      if (!own(win.history.state)) { at = Math.max(at, 0); win.history.replaceState(stamp(at), '', win.location.href); }
      if (at === 0) guard();
    }
    return () => { if (handler === fn) { handler = undefined; listen(false); } };
  };
}
