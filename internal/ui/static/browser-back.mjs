// One browser-history guard delegates to the skin's existing layer dismissal.
// It never reads messages, authorizes work, or stores a conversation in history.
export function browserBackNavigation(win = window) {
  let handler, armed = false, leaving = false, canLeave = false;
  const oldMarker = win.history.state?.agentnetBack;
  const marker = typeof oldMarker === 'string' && oldMarker.startsWith('agentnet-back-') ? oldMarker : 'agentnet-back-' + win.crypto.randomUUID();
  const push = () => win.history.pushState({ ...win.history.state, agentnetBack: marker }, '', win.location.href);
  const pop = event => {
    if (!handler || leaving || event.state?.agentnetBack === marker) return;
    if (handler()) push();
    else if (canLeave) { leaving = true; win.history.back(); }
    else push(); // a directly opened tab has no earlier document to return to
  };
  const page = () => { leaving = false; };
  win.addEventListener('popstate', pop);
  win.addEventListener('pageshow', page);
  return fn => {
    handler = fn;
    if (!armed) { canLeave = win.history.length > 1; armed = true; }
    if (win.history.state?.agentnetBack !== marker) push();
    return () => { if (handler === fn) handler = undefined; };
  };
}
