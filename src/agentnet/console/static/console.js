'use strict';

(() => {
  const live = document.querySelector('[data-live-status]');
  if (!live) return;

  let revision = Number(live.getAttribute('data-revision') || '0');
  let failures = 0;
  let pollTimer = null;

  const showFreshness = (value) => {
    if (typeof value !== 'number') return;
    const date = new Date(value * 1000);
    live.textContent = `Updated ${date.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})}`;
  };

  const apply = (event) => {
    if (!event || typeof event.revision !== 'number') return;
    if (event.revision <= revision) return;
    revision = event.revision;
    showFreshness(event.fresh_at);
    live.textContent += ' — refresh to review changes';
  };

  const schedulePoll = () => {
    if (pollTimer !== null) return;
    const interval = Math.min(60000, 5000 * Math.max(1, 2 ** failures));
    pollTimer = window.setTimeout(async () => {
      pollTimer = null;
      try {
        const response = await fetch(`/v1/console/snapshot?after=${revision}`, {
          credentials: 'same-origin',
          cache: 'no-store',
          headers: {'Accept': 'application/json'},
        });
        if (!response.ok) throw new Error('update unavailable');
        const value = await response.json();
        apply(value);
        failures = 0;
      } catch (_error) {
        failures = Math.min(failures + 1, 4);
      }
      schedulePoll();
    }, interval);
  };

  if ('EventSource' in window) {
    const stream = new EventSource(`/v1/console/events?after=${revision}`, {withCredentials: true});
    stream.addEventListener('console', (event) => {
      try {
        apply(JSON.parse(event.data));
      } catch (_error) {
        failures = Math.min(failures + 1, 4);
      }
    });
    stream.addEventListener('error', () => {
      stream.close();
      schedulePoll();
    });
  } else {
    schedulePoll();
  }
})();
