(() => {
  const invoke = window.__TAURI_INTERNALS__?.invoke;
  if (typeof invoke !== 'function') return;
  const rows = new Map();
  const nativeLinks = new WeakSet();
  // Native shell chrome is outside the skin. Give it its own system palette
  // so page resets and dark native button defaults cannot hide its controls.
  const style = new CSSStyleSheet();
  style.replaceSync(`
    #agentnet-native-downloads { color-scheme:light dark;color:CanvasText;background:Canvas;border:1px solid CanvasText; }
    :root[data-theme="light"] #agentnet-native-downloads { color-scheme:light; }
    :root[data-theme="dark"] #agentnet-native-downloads { color-scheme:dark; }
    #agentnet-native-downloads button { appearance:none;min-height:36px;margin:4px 6px 0 0;padding:8px 12px;border:1px solid ButtonText;border-radius:6px;font:inherit;font-weight:600;line-height:1.25;color:ButtonText;background:ButtonFace;cursor:pointer; }
    #agentnet-native-downloads button:not(:disabled):hover { color:Canvas;background:CanvasText;border-color:CanvasText; }
    #agentnet-native-downloads button:focus-visible { outline:3px solid CanvasText;outline-offset:2px; }
    #agentnet-native-downloads button:disabled { color:CanvasText;background:Canvas;border-style:dashed;opacity:1;cursor:default; }
  `);
  document.adoptedStyleSheets = [...document.adoptedStyleSheets, style];
  function row(url, name) {
    if (rows.has(url)) return rows.get(url);
    let tray = document.getElementById('agentnet-native-downloads');
    if (!tray) {
      tray = document.createElement('section'); tray.id = 'agentnet-native-downloads';
      tray.setAttribute('aria-label', 'Downloads');
      tray.style.cssText = 'position:fixed;right:12px;bottom:12px;width:min(360px,calc(100vw - 24px));max-height:45vh;overflow:auto;z-index:2147483647;font:14px system-ui;border-radius:10px;box-shadow:0 3px 18px #0003;padding:8px;';
      document.body.append(tray);
    }
    for (const [oldURL, old] of rows) if (old.done && rows.size >= 8) { old.node.remove(); rows.delete(oldURL); }
    const node = document.createElement('div'), title = document.createElement('strong'), status = document.createElement('p'), progress = document.createElement('progress'), close = document.createElement('button');
    node.style.cssText = 'padding:8px;overflow-wrap:anywhere;'; title.textContent = name || 'Download';
    status.setAttribute('role', 'status'); status.style.cssText = 'margin:5px 0;';
    progress.setAttribute('aria-label', 'Download progress'); progress.max = 1; progress.style.width = '100%';
    close.textContent = 'Dismiss'; close.disabled = true;
    node.append(title, status, progress, close); tray.append(node);
    const item = { node, title, status, progress, close, done: false };
    close.onclick = () => { if (item.done) { node.remove(); rows.delete(url); if (!tray.children.length) tray.remove(); } };
    rows.set(url, item);
    return item;
  }
  window.__agentnetDownloadProgress = ({ url, name, state, progress, terminal }) => {
    const item = row(url, name);
    if (name) item.title.textContent = name;
    if (item.done || item.nativeOwned && !terminal && (state === 'saved' || state === 'failed')) return;
    if (state === 'saved' || state === 'failed' || state === 'cancelled') {
      item.done = true; item.progress.hidden = true; item.close.disabled = false;
      item.status.textContent = state === 'saved' ? 'Saved to Downloads.' : state === 'cancelled' ? 'Download cancelled.' : 'Download failed. Retry from its message.';
      item.cancel?.remove();
      if (item.lease) { revoke(item.lease); item.lease = null; }
    } else {
      item.status.textContent = state === 'saving' ? 'Saving…' : 'Starting download…';
      if (typeof progress === 'number' && Number.isFinite(progress)) item.progress.value = Math.max(0, Math.min(1, progress));
      else item.progress.removeAttribute('value');
      if (item.cancel && state === 'saving') item.cancel.disabled = false;
    }
  };
  // Keep Blob references, not another body copy. Each clicked download gets
  // its own URL lease; message view unmount can revoke the original safely.
  const blobs = new Map(), create = URL.createObjectURL.bind(URL), revoke = URL.revokeObjectURL.bind(URL);
  URL.createObjectURL = blob => { const url = create(blob); if (blob instanceof Blob) blobs.set(url, blob); return url; };
  URL.revokeObjectURL = url => { blobs.delete(String(url)); revoke(url); };
  document.addEventListener('click', event => {
    const link = event.composedPath().find(node => node instanceof HTMLAnchorElement && node.hasAttribute('download'));
    if (!link || !link.href.startsWith('blob:')) return;
    if (nativeLinks.has(link)) return; // our single-use native anchor
    event.preventDefault();
    const blob = blobs.get(link.href), name = link.download;
    if (!blob || !link.href.startsWith('blob:' + location.origin + '/')) { window.alert('This download is unavailable. Open its message and try again.'); return; }
    const url = create(blob), item = row(url, name), controller = new AbortController();
    if (!window.__agentnetLinuxDownloads) {
      // Native callbacks identify a transfer by URL on every platform. Never
      // share that identity across concurrent clicks or revoke it on unmount.
      item.lease = url;
      const nativeLink = document.createElement('a'); nativeLinks.add(nativeLink);
      nativeLink.href = url; nativeLink.download = name; document.body.append(nativeLink);
      window.__agentnetDownloadProgress({ url, name, state: 'starting' });
      nativeLink.click(); nativeLink.remove();
      return;
    }
    item.nativeOwned = true;
    let cancelled = false, native = false;
    const cancel = document.createElement('button'); cancel.textContent = 'Cancel download'; item.cancel = cancel; item.node.append(cancel);
    cancel.onclick = () => {
      cancelled = true; cancel.disabled = true;
      if (native) void invoke('agentnet_cancel_download', { url }).catch(() => { cancelled = false; cancel.disabled = false; item.status.textContent = 'Could not cancel. Try again.'; });
      else controller.abort();
    };
    window.__agentnetDownloadProgress({ url, name, state: 'starting' });
    void (async () => {
      // Registration and IPC use different process lanes. Await the response,
      // cancelling its body without reading/copying the attachment again.
      const response = await fetch(url, { signal: controller.signal });
      if (!response.ok) throw new Error('Download is unavailable.');
      await response.body?.cancel();
      if (cancelled) throw new Error('Cancelled.');
      native = true; cancel.disabled = true; // enabled by actual native data
      await invoke('agentnet_download_blob', { url, name });
      window.__agentnetDownloadProgress({ url, state: 'saved', terminal: true });
    })().catch(error => window.__agentnetDownloadProgress({ url, state: !native && cancelled || String(error) === 'Download cancelled.' ? 'cancelled' : 'failed', terminal: true }))
      .finally(() => { revoke(url); }); // native failed/finished, never IPC start
  }, true);
})();
