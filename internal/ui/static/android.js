// Android's trusted local page. Native owns SQLite/identity/transport; Comic
// remains the shared UI. Only bounded, origin-scoped OS actions cross this bridge.
(() => {
  'use strict';
  const bridge = window.AgentNetAndroid;
  if (!bridge || typeof bridge.postMessage !== 'function') return;
  const waiting = new Map();
  let next = 0, back = null, exporting = false, nativeVisible = true;
  const visibility = new Set();
  const request = (type, fields = {}) => new Promise((resolve, reject) => {
    const id = String(++next);
    waiting.set(id, { resolve, reject });
    try { bridge.postMessage(JSON.stringify({ id, type, ...fields })); }
    catch (error) { waiting.delete(id); reject(error); }
  });
  bridge.onmessage = event => {
    let reply;
    try { reply = JSON.parse(event.data); } catch (_) { return; }
    const pending = waiting.get(reply.id);
    if (!pending) return;
    waiting.delete(reply.id);
    if (reply.ok) pending.resolve(reply);
    else pending.reject(new Error(reply.error || 'Android could not complete this action.'));
  };
  window.__agentnetPlatform = 'android';
  // The shell alone chooses release, URL and installer file. This hook
  // replaces the desktop-only HTTP route used by the shared loader.
  window.__agentnetNativeAppJSON = async (action, body) => {
    if (!['status', 'check', 'update'].includes(action)) throw new Error('Unsupported Android app action.');
    if (body !== undefined && (body === null || typeof body !== 'object' || Array.isArray(body) || Object.keys(body).length)) {
      throw new Error('Android updates do not accept caller-supplied installation options.');
    }
    const reply = await request('app:' + action);
    if (!reply.result || typeof reply.result !== 'object' || Array.isArray(reply.result)) throw new Error('Android returned an invalid app result.');
    return reply.result;
  };
  window.__agentnetAndroid = Object.freeze({
    connection: () => request('connection:get'),
    connectionSettings: () => request('connection:settings'),
    setConnection: enabled => request('connection:set', { enabled: !!enabled }),
    requestNotifications: () => request('notifications:request'),
    notificationStatus: () => request('notifications:status'),
    notificationSettings: () => request('notifications:settings'),
    copyText: text => request('clipboard:write', {text}),
    legacyDrafts: () => request('drafts:legacy'),
    acknowledgeLegacyDraft: (key, value) => request('drafts:ack', {key, value}),
    scanQR: () => request('qr:scan'),
    // Whether the Android app is in the foreground. The WebView's own
    // document visibility can lag the activity, so skins check both.
    visible: () => nativeVisible,
    onVisibility: fn => {
      visibility.add(fn);
      return () => { visibility.delete(fn); };
    },
  });
  window.__agentnetOnBack = fn => {
    back = fn;
    return () => { if (back === fn) back = null; };
  };
  window.agentnetNativeBack = () => back ? !!back() : false;
  window.agentnetNativeVisibility = visible => {
    nativeVisible = !!visible;
    for (const fn of [...visibility]) { try { fn(nativeVisible); } catch (_) { /* the skin's own */ } }
  };

  // The shared UI downloads already-decrypted bytes via an owned blob URL.
  // Use the Android document picker, never a raw filesystem path or URL loader.
  document.addEventListener('click', event => {
    const link = event.composedPath().find(node => node instanceof HTMLAnchorElement && node.hasAttribute('download'));
    if (!link || !link.href.startsWith('blob:' + location.origin + '/')) return;
    event.preventDefault();
    if (exporting) return;
    exporting = true;
    let transfer = '';
    (async () => {
      const response = await fetch(link.href);
      if (!response.ok) throw new Error('Could not read this attachment. Try Download again.');
      const blob = await response.blob();
      const started = await request('export:start', { name: link.download || 'attachment', mime: blob.type || 'application/octet-stream', size: blob.size });
      transfer = started.transfer;
      for (let offset = 0; offset < blob.size; offset += 32768) {
        const bytes = new Uint8Array(await blob.slice(offset, offset + 32768).arrayBuffer());
        let raw = '';
        for (const byte of bytes) raw += String.fromCharCode(byte);
        await request('export:chunk', { transfer, data: btoa(raw) });
      }
      await request('export:finish', { transfer });
      transfer = '';
    })().catch(async error => {
      if (transfer) await request('export:cancel', { transfer }).catch(() => {});
      window.alert(error.message || 'Could not save this attachment.');
    }).finally(() => { exporting = false; });
  }, true);
})();
