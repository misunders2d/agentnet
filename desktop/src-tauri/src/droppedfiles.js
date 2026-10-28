(() => {
  const invoke = window.__TAURI_INTERNALS__?.invoke;
  if (typeof invoke !== 'function') return;
  const listeners = new Set();
  const seen = new Set();
  const types = { png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp', mp4: 'video/mp4', webm: 'video/webm', mov: 'video/quicktime' };
  Object.defineProperty(window, '__agentnetSubscribeFileDrop', { value: handler => {
    listeners.add(handler);
    return () => listeners.delete(handler);
  }});
  Object.defineProperty(window, '__agentnetNativeFileDrop', { value: drop => {
    const key = (drop.files || []).map(file => file.id).join(',');
    if (key && seen.has(key)) return;
    if (key) { seen.add(key); if (seen.size > 64) seen.delete(seen.values().next().value); }
    const files = (drop.files || []).map(({ id, name }) => {
      let reading;
      return { name,
        read: () => reading ||= invoke('agentnet_dropped_file', { id, discard: false }).then(bytes => {
          const data = bytes instanceof ArrayBuffer || ArrayBuffer.isView(bytes) ? bytes : Array.isArray(bytes) ? new Uint8Array(bytes) : null;
          if (!data) throw Error('The dropped file could not be read.');
          return new File([data], name, { type: types[name.split('.').pop().toLowerCase()] || '' });
        }),
        discard: () => { if (!reading) void invoke('agentnet_dropped_file', { id, discard: true }).catch(() => {}); },
      };
    });
    for (const listener of [...listeners].reverse()) {
      try { if (listener({ x: drop.x, y: drop.y, error: drop.error, files })) return; }
      catch { /* a retired or refusing view grants nothing */ }
    }
    for (const file of files) file.discard();
  }});
})();
