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
  // A page newly navigated to has cut whatever was ahead of it, so all of history but
  // this entry is behind it, once, while nothing has been dropped. Kept on the entry,
  // this holds across reloads and returns, when the browser no longer says.
  const historyCap = 50;
  const pushed = nav?.activation ? nav.activation.navigationType === 'push' : !nav && win.performance?.getEntriesByType?.('navigation')?.[0]?.type === 'navigate';
  const before = !pushed || win.history.length >= historyCap ? undefined : win.history.length > 1 ? 'other' : 'nothing';
  const guard = () => { at += 1; win.history.pushState(stamp(at), '', win.location.href); };
  const guardFromHere = () => {
    generation += 1;
    at = 0;
    win.history.replaceState(stamp(at), '', win.location.href);
    guard();
  };
  // Firefox past its cap goes on listing the entries it dropped, oldest first, where going
  // to one does nothing. History's length says how many of them are still real.
  const firstListed = () => Math.max(nav.entries().length - win.history.length, 0);
  // What is behind this entry. Going back past the start raises no event, so a traversal
  // this page may not be sure of cannot be told from a slow one: it must know beforehand.
  // The HTML navigation API lists only same-origin entries touching this one, so history
  // longer than that list holds other origins, on a side only the page's load can tell.
  const behind = () => {
    const current = nav?.currentEntry, state = win.history.state;
    if (current && current.index > firstListed()) return 'own';
    if (current && win.history.length <= nav.entries().length) return 'nothing';
    // Past the cap, entries were dropped from behind: what the load saw no longer holds.
    // So too when places count more entries than history has.
    if (!own(state) || (!current && (win.history.length >= historyCap || state.agentnetBackSeq >= win.history.length))) return 'unknown';
    if (state.agentnetBackSeq === 0) return state.agentnetBackBefore || 'unknown';
    return current ? 'unknown' : 'other';
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
  // Places count entries the browser may have dropped or a writer re-marked, so a distance
  // worked out from them can point past the start of history, where going back does nothing
  // at all, and no event says so. So an engine without the API walks one entry at a time,
  // past an older version's unmarked entries as well. With the API the browser lists the
  // entries, and leaving is one hop to the entry before this document's, however many it has.
  const leaveStep = () => {
    win.clearTimeout(giveUp);
    const there = behind();
    if (there === 'nothing') { walking = false; canLeave = false; guardFromHere(); return; }
    walking = true;
    if (there === 'own') {
      const refused = () => { if (walking) { walking = false; mayReturnOnInput(); } };
      const list = nav.entries(), first = firstListed();
      let to = nav.currentEntry.index;
      while (to > first && list[to - 1].sameDocument) to--;
      const step = nav.traverseTo(list[Math.max(to - 1, first)].key);
      step.committed?.catch(refused);
      step.finished?.catch(() => {});
      return;
    }
    // Only here is a slow traversal the same as none at all: a tab from before this
    // version, or an engine without the API at the cap. Input past a second brings the guard.
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
    if (handler && at === 0) guard(); // returned from the page cache onto the base entry: no skin arms again
  };
  win.addEventListener('popstate', pop);
  win.addEventListener('pageshow', page);
  return fn => {
    handler = fn;
    if (!armed) { canLeave = win.history.length > Math.max(at, 0) + 1; armed = true; }
    if (returning) listen(true);
    if (!walking) {
      if (!own(win.history.state)) { at = Math.max(at, 0); win.history.replaceState({ ...stamp(at), ...(before && at === 0 ? { agentnetBackBefore: before } : {}) }, '', win.location.href); }
      if (at === 0) guard();
    }
    return () => { if (handler === fn) { handler = undefined; listen(false); } };
  };
}
